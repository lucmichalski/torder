package main

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"sync"
	"syscall"
	"time"

	"github.com/JesusIslam/tldr"
	"github.com/go-sql-driver/mysql"
	"github.com/gocolly/redisstorage"
	"github.com/jinzhu/gorm"
	_ "github.com/jinzhu/gorm/dialects/mysql"
	"github.com/k0kubun/pp"
	"github.com/kelseyhightower/envconfig"
	"github.com/qor/media"
	"github.com/qor/validations"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/pflag"

	"github.com/lucmichalski/torder/internal/spider"
	ccsv "github.com/lucmichalski/torder/pkg/csv"
	"github.com/lucmichalski/torder/pkg/gowap"
	"github.com/lucmichalski/torder/pkg/serviceregistry"
)

type config struct {
	RedisURI          string `envconfig:"REDIS_URI" required:"true"`
	ElasticURI        string `envconfig:"ELASTIC_URI" required:"true"`
	ElasticIndex      string `envconfig:"ELASTIC_INDEX" required:"true"`
	ProxyURI          string `envconfig:"PROXY_URI" required:"true"`
	MongoURI          string `envconfig:"MONGO_URI" required:"true"`
	MongoDB           string `envconfig:"MONGO_DB" required:"true"`
	MongoCol          string `envconfig:"MONGO_COL" required:"true"`
	LogLevel          string `envconfig:"LOG_LEVEL" default:"error"`
	BlacklistFile     string `envconfig:"BLACKLIST_FILE" required:"false"`
	Depth             int    `envconfig:"DEPTH" default:"3"`
	Workers           int    `envconfig:"WORKERS" default:"32"`
	Parallelism       int    `envconfig:"PARALLELISM" default:"16"`
	LogStatusInterval int    `envconfig:"LOG_STATUS_INTERVAL" default:"3"`
	RdbmsDriver       string `envconfig:"TOR_MYSQL_DRIVER"`
	RdbmsUser         string `envconfig:"TOR_MYSQL_USER"`
	RdbmsPassword     string `envconfig:"TOR_MYSQL_PASSWORD"`
	RdbmsHost         string `envconfig:"TOR_MYSQL_HOST"`
	RdbmsPort         string `envconfig:"TOR_MYSQL_PORT"`
	RdbmsDatabase     string `envconfig:"TOR_MYSQL_DATABASE"`
}

var (
	isHelp    bool
	isVerbose bool
	seedsFile string
)

func main() {
	// flag management
	pflag.StringVarP(&seedsFile, "seeds-file", "s", "./shared/dataset/deepquest.csv", "bulk load seeds from csv file.")
	pflag.BoolVarP(&isVerbose, "verbose", "v", false, "verbose mode.")
	pflag.BoolVarP(&isHelp, "help", "h", false, "help info.")
	pflag.Parse()
	if isHelp {
		pflag.PrintDefaults()
		os.Exit(1)
	}

	logger := log.New()
	logger.SetFormatter(&log.TextFormatter{
		FullTimestamp: true,
	})

	config, err := loadConfig()
	if err != nil {
		logger.Fatal(err)
	}

	switch {
	case config.LogLevel == "error":
		logger.SetLevel(log.ErrorLevel)
	case config.LogLevel == "info":
		logger.SetLevel(log.InfoLevel)
	case config.LogLevel == "debug":
		logger.SetLevel(log.DebugLevel)
	default:
		logger.Fatalf("Invalid debug level: %s", config.LogLevel)
	}

	proxyURL, err := url.Parse(config.ProxyURI)
	if err != nil {
		log.Panic(err)
	}

	visitedStorage := &redisstorage.Storage{
		Address:  config.RedisURI,
		Password: "",
		DB:       0,
		Prefix:   "0",
	}

	defer func() {
		visitedStorage.Client.Close()
	}()

	// Setting up RDBMS
	db, err := gorm.Open("mysql", fmt.Sprintf("%v:%v@tcp(%v:%v)/%v?charset=utf8mb4,utf8&parseTime=True", os.Getenv("TOR_MYSQL_USER"), os.Getenv("TOR_MYSQL_PASSWORD"), os.Getenv("TOR_MYSQL_HOST"), os.Getenv("TOR_MYSQL_PORT"), os.Getenv("TOR_MYSQL_DATABASE")))
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// callback for images and validation
	validations.RegisterCallbacks(db)
	media.RegisterCallbacks(db)

	// migrate tables
	db.AutoMigrate(&spider.PageInfo{}).AddUniqueIndex("url", "url").AddUniqueIndex("fingerprint", "fingerprint")
	db.AutoMigrate(&spider.Outbound{})
	db.AutoMigrate(&spider.PageTopic{})
	db.AutoMigrate(&spider.PageAttribute{})
	db.AutoMigrate(&spider.Tag{})
	db.AutoMigrate(&spider.Service{})
	db.AutoMigrate(&spider.URL{})
	db.AutoMigrate(&spider.PublicKey{})
	db.AutoMigrate(&spider.Seed{}).AddUniqueIndex("url", "url")
	db.AutoMigrate(&spider.Job{}).AddUniqueIndex("url", "url")

	// bulk load seeds
	if seedsFile != "" {
		if _, err := os.Stat(seedsFile); !os.IsNotExist(err) {
			log.Infof("Processing seeds file found at: %s", seedsFile)
			loadSeeds(seedsFile, db)
		} else {
			log.Warnf("No seeds file found at: %s", seedsFile)
		}
	}

	// instanciate gowap dictionary
	wapp, err := gowap.Init("./shared/dataset/wappalyzer/apps.json", config.ProxyURI, false)
	if err != nil {
		log.Fatal(err)
	}

	bitcoinPatternRegexp, err := regexp.Compile(`[13][a-km-zA-HJ-NP-Z0-9]{26,33}$`)
	if err != nil {
		log.Fatal(err)
	}

	emailPatternRegexp, err := regexp.Compile(`([a-zA-Z0-9_\-\.]+)@([a-zA-Z0-9_\-\.]+)\.([a-zA-Z]{2,5})$`)
	if err != nil {
		log.Fatal(err)
	}

	onionPatternRegexp, err := regexp.Compile(`(?:https?://)?(?:www)?(\S*?\.onion)\b`)
	if err != nil {
		log.Fatal(err)
	}

	twitterPatternRegexp, err := regexp.Compile(`(https?\:)?(//)(www[\.])?(twitter.com/)([a-zA-Z0-9_]{1,15})[\/]?`)
	if err != nil {
		log.Fatal(err)
	}

	registry := serviceregistry.NewServiceRegistry()

	elasticPageStorage, err := spider.NewElasticPageStorage(config.ElasticURI, config.ElasticIndex, 100)

	if err != nil {
		logger.Fatal(err)
	}

	elasticPageStorage.Logger = logger

	mongoJobsStorage, err := spider.NewMongoJobsStorage(config.MongoURI, config.MongoDB, config.MongoCol, 1000, config.Workers)

	if err != nil {
		logger.Fatal(err)
	}

	mongoJobsStorage.Logger = logger

	if err := registry.RegisterService(elasticPageStorage); err != nil {
		logger.Fatal(err)
	}

	if err := registry.RegisterService(mongoJobsStorage); err != nil {
		logger.Fatal(err)
	}

	// init lexrank
	bag := tldr.New()

	// init concurrent csv writer
	csvWriter, err := ccsv.NewCsvWriter("shared/dumps/data.csv", '\t')
	if err != nil {
		logger.Fatal(err)
	}

	// Flush pending writes and close file upon exit of Sitemap()
	defer csvWriter.Close()

	// TODO use generic interface as it's meant to be done. For now this will
	// do.
	var js *spider.MongoJobsStorage
	if err := registry.FetchService(&js); err != nil {
		logger.Fatal(err)
	}

	var ps *spider.ElasticPageStorage
	if err := registry.FetchService(&ps); err != nil {
		logger.Fatal(err)
	}

	spider := &spider.Spider{
		Rdbms:        db,
		Wapp:         wapp,
		CsvWriter:    csvWriter,
		RegexTwitter: twitterPatternRegexp,
		RegexBitcoin: bitcoinPatternRegexp,
		RegexEmail:   emailPatternRegexp,
		RegexOnion:   onionPatternRegexp,
		Storage:      visitedStorage,
		Bag:          bag,
		JS:           js,
		PS:           ps,
		ProxyURL:     proxyURL,
		NumWorkers:   config.Workers,
		Parallelism:  config.Parallelism,
		Depth:        config.Depth,
		Logger:       logger,
	}

	if err := spider.Init(); err != nil {
		log.Fatalf("Spider ended with %v", err)
	}

	// temporary will start it here
	// spider.AnalyzeSeeds()
	// os.Exit(1)

	pp.Println("config.BlacklistFile:", config.BlacklistFile)
	if config.BlacklistFile != "" {
		blacklist, err := readLines(config.BlacklistFile)
		if err != nil {
			log.Fatal("Error while reading " + config.BlacklistFile)
		}
		spider.Blacklist = blacklist
		pp.Println("spider.Blacklist:", spider.Blacklist)
	}

	if err := registry.RegisterService(spider); err != nil {
		logger.Fatal(err)
	}

	registry.StartAll()

	stop := make(chan os.Signal, 1)
	done := make(chan struct{})
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	ticker := time.NewTicker(time.Second * time.Duration(config.LogStatusInterval))

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		<-stop
		registry.StopAll()
		close(done)
		wg.Done()
	}()

	for {
		select {
		case <-ticker.C:
			statuses := registry.Statuses()
			for kind, status := range statuses {
				logger.Infof("%s %s", kind, status)
			}
			fmt.Println()
		case <-done:
			statuses := registry.Statuses()
			fmt.Println("All services stopped")
			for kind, status := range statuses {
				logger.Infof("%s %s", kind, status)
			}
			wg.Wait()
			return
		}
	}
}

func loadSeeds(csvFile string, DB *gorm.DB) {
	fmt.Println("loading data from file...")
	mysql.RegisterLocalFile(csvFile)
	query := `LOAD DATA LOCAL INFILE '` + csvFile + `' INTO TABLE seeds CHARACTER SET 'utf8mb4' FIELDS TERMINATED BY '\t' ENCLOSED BY '"' LINES TERMINATED BY '\n' (url) SET created_at = NOW(), updated_at = NOW();`
	fmt.Println(query)
	err := DB.Exec(query).Error
	if err != nil {
		log.Fatal(err)
	}
}

func loadConfig() (*config, error) {
	var cfg config
	err := envconfig.Process("", &cfg)
	if err != nil {
		return nil, err
	}
	return &cfg, nil
}

func readLines(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}
