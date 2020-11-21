package spider

import (
	"context"
	"crypto/md5"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JesusIslam/tldr"
	"github.com/PuerkitoBio/goquery"
	"github.com/abadojack/whatlanggo"
	"github.com/gin-gonic/gin"
	"github.com/gocolly/colly/v2"
	"github.com/gocolly/colly/v2/extensions"
	// "github.com/gocolly/colly/v2/proxy"
	"github.com/gocolly/colly/v2/queue"
	"github.com/gocolly/colly/v2/storage"
	"github.com/jinzhu/gorm"
	tld "github.com/jpillora/go-tld"
	"github.com/k0kubun/pp"
	"github.com/qor/admin"
	"github.com/qor/assetfs"
	"github.com/qor/qor/utils"
	"github.com/qor/validations"
	log "github.com/sirupsen/logrus"
	"gopkg.in/jdkato/prose.v2"

	"github.com/lucmichalski/torder/pkg/articletext"
	ccsv "github.com/lucmichalski/torder/pkg/csv"
	"github.com/lucmichalski/torder/pkg/gowap"
	"github.com/lucmichalski/torder/pkg/manticore"
)

/*
	- https://github.com/x0rzkov/SpaghettiSearch
		- Topic-Sensitive PageRank (T. H. Haveliwala, 2003)
*/

// mainly baniry files
var excludeExtension = []string{
	".m3u",
	".torrent",
	".mp3",
	".mp4",
	".mov",
	".wav",
	".avi",
	".jpg",
	".jpeg",
	".png",
	".bmp",
	".gif",
	".wmf",
	".iso",
	".pdf",
	".doc",
	".ppt",
	".pptx",
	".epub",
	".mobi",
	".zip",
	".rar",
	".tar.gz",
	".tgz",
	".bz2",
	".7z",
	".rdf",
	".md",
}

// Job is a struct that represents a job
type Job struct {
	gorm.Model
	URL string `gorm:"type:varchar(768); CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci"`
}

// Outbound represents outbound links
type Outbound struct {
	gorm.Model
	Origin      string `gorm:"type:varchar(768); CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci"`
	Destination string `gorm:"type:varchar(768); CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci"`
}

// PageInfo is a struct used to save the informations about a visited page
type PageInfo struct {
	ID             uint            `gorm:"primary_key" json:"-"`
	CreatedAt      time.Time       `json:"-"`
	UpdatedAt      time.Time       `json:"-"`
	DeletedAt      *time.Time      `sql:"index" json:"-"`
	URL            string          `gorm:"index:url"`
	Body           string          `gorm:"type:longtext; CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci" sql:"type:longtext"`
	LexRank        string          `gorm:"type:longtext; CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci" sql:"type:longtext"`
	Summary        string          `gorm:"type:longtext; CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci" sql:"type:longtext"`
	Title          string          `gorm:"type:longtext; CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci" sql:"type:longtext"`
	Keywords       string          `gorm:"type:longtext; CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci" sql:"type:longtext"`
	Category       string          `gorm:"index:category"`
	Domain         string          `gorm:"index:domain"`
	IsHomePage     bool            `gorm:"index:home_page"`
	Status         int             `gorm:"index:status"`
	Language       string          `gorm:"index:language"`
	LangConfidence float64         `json:"-"`
	Fingerprint    string          `json:"-" gorm:"index:fingerprint"`
	Wapp           string          `gorm:"type:longtext; CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci" sql:"type:longtext" json:"-"`
	PageTopic      []*PageTopic    `gorm:"many2many:page_topics;" json:"-"`
	PageProperties PageProperties  `sql:"type:text" json:"-"`
	PageAttributes []PageAttribute `json:"-"`
}

func (p *PageInfo) BeforeCreate() (err error) {
	if p.Summary != "" {
		info := whatlanggo.Detect(p.Summary)
		p.Language = info.Lang.String()
		p.LangConfidence = info.Confidence
		fmt.Println("======> Language:", p.Language, " Script:", whatlanggo.Scripts[info.Script], " Confidence: ", p.LangConfidence)
	}
	return
}

type PageProperties []PageProperty

type PageProperty struct {
	Name  string
	Value string
}

func (pageProperties *PageProperties) Scan(value interface{}) error {
	switch v := value.(type) {
	case []byte:
		return json.Unmarshal(v, pageProperties)
	case string:
		if v != "" {
			return pageProperties.Scan([]byte(v))
		}
	default:
		return errors.New("not supported")
	}
	return nil
}

func (pageProperties PageProperties) Value() (driver.Value, error) {
	if len(pageProperties) == 0 {
		return nil, nil
	}
	return json.Marshal(pageProperties)
}

type PageAttribute struct {
	gorm.Model
	PageInfoID uint
	Name       string
	Value      string
}

func (p PageAttribute) Validate(db *gorm.DB) {
	if strings.TrimSpace(p.Name) == "" {
		db.AddError(validations.NewError(p, "Name", "Name can not be empty"))
	}
	if strings.TrimSpace(p.Value) == "" {
		db.AddError(validations.NewError(p, "Value", "Value can not be empty"))
	}
}

// PageTopic is a struct used to store topics detected in a visited page (WIP)
type PageTopic struct {
	gorm.Model
	PageInfoID     uint
	Name           string `gorm:"index:name"`
	Language       string `gorm:"index:language"`
	LangConfidence float64
}

type Seed struct {
	gorm.Model
	URL            string
	Title          string  `gorm:"type:longtext; CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci" sql:"type:longtext"`
	Description    string  `gorm:"type:longtext; CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci" sql:"type:longtext"`
	Summary        string  `gorm:"type:longtext; CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci" sql:"type:longtext"`
	Status         int     `gorm:"index:status"`
	Language       string  `gorm:"index:language"`
	LangConfidence float64 `json:"-"`
	Fingerprint    string  `gorm:"index:fingerprint"`
	Visited        bool
	Crawled        bool
}

func (s *Seed) BeforeSave() (err error) {
	if s.Summary != "" {
		info := whatlanggo.Detect(s.Summary)
		s.Language = info.Lang.String()
		s.LangConfidence = info.Confidence
		fmt.Println("======> Language:", s.Language, " Script:", whatlanggo.Scripts[info.Script], " Confidence: ", s.LangConfidence)
	}
	return
}

// PageStorage is an interface which handles tha storage of the visited pages
type PageStorage interface {
	SavePage(PageInfo)
}

// JobsStorage is an interface which handles tha storage of the visited pages
type JobsStorage interface {
	SaveJob(Job)
	GetJob() (Job, error)
}

// Spider is a struct that represents a Spider
type Spider struct {
	NumWorkers  int
	Parallelism int
	Depth       int
	Blacklist   []string
	ProxyURL    *url.URL
	Logger      *log.Logger

	Storage storage.Storage
	JS      JobsStorage
	PS      PageStorage

	RegexTwitter *regexp.Regexp
	RegexOnion   *regexp.Regexp
	RegexBitcoin *regexp.Regexp
	RegexEmail   *regexp.Regexp

	CsvWriter *ccsv.CsvWriter
	Bag       *tldr.Bag
	Manticore manticore.Client
	Rdbms     *gorm.DB
	Wapp      *gowap.Wappalyzer

	wg                    *sync.WaitGroup
	sem                   chan struct{}
	runningCollectors     chan struct{}
	runningSeedCollectors chan struct{}
	done                  chan struct{}
	torCollector          *colly.Collector
}

// Init initialized all the struct values
func (spider *Spider) Init() error {
	spider.sem = make(chan struct{}, spider.NumWorkers)
	spider.runningCollectors = make(chan struct{}, spider.NumWorkers)
	spider.runningSeedCollectors = make(chan struct{}, spider.NumWorkers)
	spider.done = make(chan struct{})
	spider.wg = &sync.WaitGroup{}

	err := spider.initCollector()

	if err != nil {
		return err
	}

	return nil
}

func (spider *Spider) initCollector() error {
	disallowed := make([]*regexp.Regexp, len(spider.Blacklist))

	for index, b := range spider.Blacklist {
		disallowed[index] = regexp.MustCompile(b)
	}

	c := colly.NewCollector(
		colly.MaxDepth(spider.Depth),
		colly.Async(true),
		colly.IgnoreRobotsTxt(),
		colly.DisallowedURLFilters(
			disallowed...,
		),
		colly.URLFilters(
			regexp.MustCompile(`http://[a-zA-Z2-7]{16}\.onion.*`),
			regexp.MustCompile(`http://[a-zA-Z2-7]{56}\.onion.*`),
			regexp.MustCompile(`https://[a-zA-Z2-7]{16}\.onion.*`),
			regexp.MustCompile(`https://[a-zA-Z2-7]{56}\.onion.*`),
		),
	)

	c.MaxBodySize = 1000 * 1000

	extensions.RandomUserAgent(c)
	extensions.Referer(c)

	c.WithTransport(&http.Transport{
		Proxy: http.ProxyURL(spider.ProxyURL),
		DialContext: (&net.Dialer{
			Timeout: 30 * time.Second,
			// KeepAlive: 30 * time.Second,
			DualStack: true,
		}).DialContext,
		MaxIdleConns:          100,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DisableKeepAlives:     true,
	})

	c.Limit(&colly.LimitRule{
		DomainGlob:  "*",
		Parallelism: spider.Parallelism,
	})

	if err := c.SetStorage(spider.Storage); err != nil {
		return err
	}

	spider.torCollector = c

	return nil
}

func (spider *Spider) startWebAdmin() {
	// Web listener
	addr := ":8889"

	// Initialize AssetFS
	AssetFS := assetfs.AssetFS().NameSpace("admin")

	// Register custom paths to manually saved views
	AssetFS.RegisterPath(filepath.Join(utils.AppRoot, "./templates/qor/admin/views"))
	AssetFS.RegisterPath(filepath.Join(utils.AppRoot, "./templates/qor/media/views"))

	// Initialize Admin
	Admin := admin.New(&admin.AdminConfig{
		SiteName: "Tor Dataset",
		DB:       spider.Rdbms,
		AssetFS:  AssetFS,
	})

	// Allow to use Admin to manage Tag, PublicKey, URL, Service
	page := Admin.AddResource(&PageInfo{})
	page.IndexAttrs("ID", "Title", "Language", "URL")

	page.Meta(&admin.Meta{
		Name: "Body",
		Type: "text",
	})

	page.Meta(&admin.Meta{
		Name: "Summary",
		Type: "rich_editor",
	})

	svc := Admin.AddResource(&Service{})
	svc.Meta(&admin.Meta{
		Name: "Description",
		Type: "rich_editor",
	})

	pks := Admin.AddResource(&PublicKey{})
	pks.Meta(&admin.Meta{
		Name: "Value",
		Type: "text",
	})

	Admin.AddResource(&URL{})

	Admin.AddResource(&Tag{})

	// initalize an HTTP request multiplexer
	mux := http.NewServeMux()

	// Mount admin interface to mux
	Admin.MountTo("/admin", mux)

	router := gin.Default()

	// add route to home page
	router.GET("/", func(c *gin.Context) {
		c.String(200, "welcome to your doom.")
	})

	// add route to search page
	router.GET("/search", func(c *gin.Context) {
		c.String(500, "not implemented yet")
	})

	// add route to add new website
	router.GET("/add", func(c *gin.Context) {
		inputUrl, _ := c.GetQuery("url")
		if inputUrl == "" {
			c.String(500, "missing url parameter.")
			return
		}
		spider.Logger.Debugf("Crawling URL: %s", inputUrl)
		collector, err := spider.getSeedCollector()
		if err != nil {
			spider.Logger.Error(err)
			return
		}
		spider.wg.Add(1)
		go func() {
			defer spider.wg.Done()
			collector.Visit(inputUrl)
			spider.Logger.Infof("Seed collector started on %s", inputUrl)
			collector.Wait()
			spider.Logger.Infof("Seed collector on %s ended", inputUrl)
		}()
		c.String(200, "ok")
	})

	// add basic auth
	admin := router.Group("/admin", gin.BasicAuth(gin.Accounts{"tor": "xor"}))
	{
		admin.Any("/*resources", gin.WrapH(mux))
	}

	s := &http.Server{
		Addr:           addr,
		Handler:        router,
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   10 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}
	spider.wg.Add(1)
	go s.ListenAndServe()
	go func() {
		<-spider.done
		s.Shutdown(context.Background())
		spider.wg.Done()
	}()
	log.Infof("Listening on %s", addr)
}

// curl --silent http://localhost:8080/seed?url=http://76qugh5bey5gum7l.onion

func (spider *Spider) startWebServer() {
	addr := ":8080"
	m := http.NewServeMux()
	s := http.Server{Addr: addr, Handler: m}

	m.HandleFunc("/seed", spider.seedHandler)
	m.HandleFunc("/periodic", spider.periodicJobHandler)

	spider.wg.Add(1)

	go s.ListenAndServe()

	go func() {
		<-spider.done
		s.Shutdown(context.Background())
		spider.wg.Done()
	}()

	log.Infof("Listening on %s", addr)
}

func (spider *Spider) seedHandler(w http.ResponseWriter, r *http.Request) {
	URL := r.URL.Query().Get("url")

	if URL == "" {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Missing url"))
		return
	}

	spider.Logger.Infof("Start seed collector for URL: %s", URL)
	err := spider.startSeedCollector(URL)

	if err != nil {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(err.Error()))
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Ok"))
}

func (spider *Spider) periodicJobHandler(w http.ResponseWriter, r *http.Request) {
	URL := r.URL.Query().Get("url")

	if URL == "" {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Missing url"))
		return
	}

	_, err := url.ParseRequestURI(URL)

	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Invalid url"))
		return
	}

	interval := r.URL.Query().Get("interval")

	if interval == "" {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Missing interval"))
		return
	}

	integerInterval, err := strconv.Atoi(interval)

	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Invalid interval"))
		return
	}

	durationInterval := time.Duration(integerInterval) * time.Second
	go spider.startPeriodicCollector(URL, durationInterval)

	response := fmt.Sprintf("Added periodic job for %s with interval of %d seconds", URL, integerInterval)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(response))
}

func isExcludedExtension(input string, dict []string) bool {
	input = strings.ToLower(input)
	for _, extension := range dict {
		if strings.HasSuffix(input, extension) {
			return true
		}
	}
	return false
}

func (spider *Spider) getCollector() (*colly.Collector, error) {

	c := spider.torCollector.Clone()

	// Get all the links
	c.OnHTML("a[href]", func(e *colly.HTMLElement) {

		// rootURL for page rank
		// rootURL := e.Request.Ctx.Get("url")

		foundURL := e.Request.AbsoluteURL(e.Attr("href"))
		// filter extensions
		if isExcludedExtension(foundURL, excludeExtension) {
			spider.Logger.Debugf("[isExcludedExtension] foundURL=%s", foundURL)
			return
		}
		// check if URL exists in db
		var pageExists PageInfo
		if !spider.Rdbms.Where("url = ?", foundURL).First(&pageExists).RecordNotFound() {
			spider.Logger.Debugf("skipping link=%s already exists\n", foundURL)
			return
		}
		if foundURL != "" && e.Request.Depth == spider.Depth && isOnion(foundURL) {
			spider.JS.SaveJob(Job{URL: foundURL})
		} else {
			e.Request.Visit(foundURL)
		}

	})

	// Save result
	c.OnResponse(func(r *colly.Response) {

		if isExcludedExtension(r.Request.URL.String(), excludeExtension) {
			spider.Logger.Debugf("[isExcludedExtension] foundURL=%s", r.Request.URL.String())
			return
		}

		// check if URL exists in db
		// nb. below we check if simalar content has been indexed
		var pageExists PageInfo
		if !spider.Rdbms.Where("url = ?", r.Request.URL.String()).First(&pageExists).RecordNotFound() {
			spider.Logger.Debugf("skipping link=%s already exists\n", r.Request.URL.String())
			return
		}

		title := ""
		body := string(r.Body)
		bodyReader := strings.NewReader(body)
		dom, err := goquery.NewDocumentFromReader(bodyReader)

		if err != nil {
			spider.Logger.Error(err)
		} else {
			title = dom.Find("title").Contents().Text()
		}

		if title == "" {
			spider.Logger.Error(errors.New("not an html page: " + r.Request.URL.String()))
			return
		}

		text, err := articletext.GetArticleTextFromDocument(dom)
		if err != nil {
			spider.Logger.Error(err)
		}

		// extract the domain vanity hash
		u, _ := tld.Parse(r.Request.URL.String())
		spider.Logger.Debugf("[parseDomain] subdomain=%s, domain=%s", u.Subdomain, u.Domain)

		// extract a md5 hash of the text to avoid duplicate content (login pages, captachas,...)
		fingerprint := strToMD5(text)

		// check if home page
		home, err := url.Parse(r.Request.URL.String())
		if err != nil {
			spider.Logger.Error(err)
		}

		result := &PageInfo{
			URL:         r.Request.URL.String(),
			Summary:     text,
			Domain:      u.Domain,
			Status:      r.StatusCode,
			Title:       title,
			Fingerprint: fingerprint,
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		}

		// var isHomePage bool
		if home.RequestURI() == "" || home.RequestURI() == "/" {
			result.IsHomePage = true
			// gowap the tor-website
			res, err := spider.Wapp.Analyze(r.Request.URL.String())
			if err != nil {
				spider.Logger.Error(err)
			}
			// prettyJSON, err := json.MarshalIndent(res, "", "  ")
			wappJson, err := json.Marshal(res)
			if err != nil {
				spider.Logger.Error(err)
			}
			result.Wapp = string(wappJson)
		}

		// check for bitcoins and email addresses
		emails := spider.RegexEmail.FindAllString(body, -1)
		emails = removeDuplicates(emails)
		for _, email := range emails {
			result.PageAttributes = append(result.PageAttributes, PageAttribute{Name: "email", Value: email})
			result.PageProperties = append(result.PageProperties, PageProperty{Name: "email", Value: email})
		}
		bitcoins := spider.RegexBitcoin.FindAllString(body, -1)
		bitcoins = removeDuplicates(bitcoins)
		for _, bitcoin := range bitcoins {
			result.PageAttributes = append(result.PageAttributes, PageAttribute{Name: "bitcoin", Value: bitcoin})
			result.PageProperties = append(result.PageProperties, PageProperty{Name: "bitcoin", Value: bitcoin})
		}

		twitters := spider.RegexTwitter.FindAllString(body, -1)
		twitters = removeDuplicates(twitters)
		for _, twitter := range twitters {
			result.PageAttributes = append(result.PageAttributes, PageAttribute{Name: "twitter", Value: twitter})
			result.PageProperties = append(result.PageProperties, PageProperty{Name: "twitter", Value: twitter})
		}

		/*
			onions := spider.regexOnion.FindAllString(body, -1)
			for _, onion := range onions {
				result.PageAttributes = append(result.PageAttributes, PageAttribute{Name: "outbound", Value: onion})
				result.PageProperties = append(result.PageProperties, PageProperty{Name: "outbound", Value: onion})
				spider.jobs <- Job{onion}
			}
		*/

		// keywords
		var topicsProse []string
		doc, _ := prose.NewDocument(text)
		for _, ent := range doc.Entities() {
			spider.Logger.Debugf("[entity] ent.Text=%s, ent.Label=%s", ent.Text, ent.Label)
			topic := ent.Text
			if len(topic) > 16 {
				continue
			}
			if topic != "" {
				topicsProse = append(topicsProse, topic)
			}
		}
		topicsProse = removeDuplicates(topicsProse)
		result.Keywords = strings.Join(topicsProse, ",")

		// lexrank
		/*
			intoSentences := 5
				lexrank, err := spider.Bag.Summarize(text, intoSentences)
				if err != nil {
					spider.Logger.Error(err)
					return
				}
				spider.Logger.Debugf("[entity] lexrank=%s", result)
				result.LexRank = strings.Join(lexrank, "|")
		*/

		if spider.CsvWriter != nil {
			spider.CsvWriter.Write([]string{
				result.URL,
				result.Summary,
				result.Domain,
				fmt.Sprintf("%d", result.Status),
				result.Title,
				result.Fingerprint,
				fmt.Sprintf("%v", result.CreatedAt),
				fmt.Sprintf("%v", result.UpdatedAt),
				fmt.Sprintf("%t", result.IsHomePage),
				result.Wapp,
				result.Keywords,
				// strings.Join(lexrank, "|"),
			})
			spider.CsvWriter.Flush()
		}

		if !spider.Rdbms.Where("fingerprint = ?", fingerprint).First(&pageExists).RecordNotFound() {
			spider.Logger.Debugf("skipping link=%s as similar content already exists\n", r.Request.URL.String())
			// if simalar content exists, skip from mysql and elasticsearch indexation
			return
		} else {
			spider.Logger.Debug("Insert into db...")
			err = spider.Rdbms.Create(result).Error
			if err != nil {
				spider.Logger.Error(err)
				return
			}
		}

		spider.PS.SavePage(*result)
	})

	c.OnRequest(func(r *colly.Request) {
		r.Ctx.Put("url", r.URL.String())
	})

	// Debug responses
	c.OnResponse(func(r *colly.Response) {
		spider.Logger.Debugf("Got %d for %s", r.StatusCode, r.Request.URL)
	})

	// Debug errors
	c.OnError(func(r *colly.Response, err error) {
		spider.Logger.Debugf("Error while visiting %s: %v", r.Request.URL, err)
	})

	return c, nil
}

func (spider *Spider) getSeedCollector() (*colly.Collector, error) {
	c := colly.NewCollector(
		colly.MaxDepth(2),
		colly.Async(true),
		colly.IgnoreRobotsTxt(),
	)

	c.MaxBodySize = 1000 * 1000

	extensions.RandomUserAgent(c)
	extensions.Referer(c)

	c.WithTransport(&http.Transport{
		Proxy: http.ProxyURL(spider.ProxyURL),
		DialContext: (&net.Dialer{
			Timeout: 30 * time.Second,
			// KeepAlive: 30 * time.Second,
			DualStack: true,
		}).DialContext,
		MaxIdleConns:          100,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DisableKeepAlives:     true,
	})

	c.Limit(&colly.LimitRule{
		DomainGlob:  "*",
		Parallelism: spider.Parallelism,
	})

	// Get all the links
	c.OnHTML("a[href]", func(e *colly.HTMLElement) {
		foundURL := e.Request.AbsoluteURL(e.Attr("href"))
		// filter extensions
		if isExcludedExtension(foundURL, excludeExtension) {
			spider.Logger.Debugf("[isExcludedExtension] foundURL=%s", foundURL)
			return
		}
		var pageExists PageInfo
		if !spider.Rdbms.Where("url = ?", foundURL).First(&pageExists).RecordNotFound() {
			spider.Logger.Debugf("skipping link=%s already exists\n", foundURL)
			return
		}
		if isOnion(foundURL) {
			spider.JS.SaveJob(Job{URL: foundURL})
		}
		e.Request.Visit(foundURL)
	})

	// Debug responses
	c.OnResponse(func(r *colly.Response) {
		spider.Logger.Debugf("SeedCollector got %d for %s", r.StatusCode,
			r.Request.URL)
	})

	// Debug errors
	c.OnError(func(r *colly.Response, err error) {
		spider.Logger.Debugf("SeedCollector: %s", err)
	})

	return c, nil
}

// Start starts the crawlers and logs messages
func (spider *Spider) Start() {
	spider.startWebServer()
	spider.startWebAdmin()
	// TODO add wg here as it's a goroutine
	for {
		select {
		case spider.sem <- struct{}{}:
			go spider.startCollector()
		case <-spider.done:
			spider.wg.Wait()
			return
		}
	}
}

// Stop signals to all goroutines to stop and waits for them to stop
func (spider *Spider) Stop() error {
	close(spider.done)
	spider.Logger.Infof("Spider is stopping")
	spider.wg.Wait()
	spider.Logger.Info("All goroutines ended, flushing data")
	return nil
}

// Status returns how many collector are running
func (spider *Spider) Status() string {
	var b strings.Builder
	b.Grow(40)

	select {
	case <-spider.done:
		fmt.Fprint(&b, "Stopped. ")
	default:
		fmt.Fprint(&b, "Running. ")
	}

	fmt.Fprintf(&b, "%dx%d collectors running and %dx%d seed collectors running", len(spider.runningCollectors), spider.Parallelism, len(spider.runningSeedCollectors),
		spider.Parallelism)

	return b.String()
}

func isOnion(url string) bool {
	u, err := tld.Parse(url)
	return err == nil && u.TLD == "onion"
}

func removeDuplicates(elements []string) []string {
	// Use map to record duplicates as we find them.
	encountered := map[string]bool{}
	result := []string{}

	for v := range elements {
		if encountered[elements[v]] == true {
			// Do not add duplicate.
		} else {
			// Record this element as an encountered element.
			encountered[elements[v]] = true
			// Append to result slice.
			result = append(result, elements[v])
		}
	}
	// Return the new slice.
	return result
}

func strToMD5(text string) string {
	hash := md5.Sum([]byte(text))
	return hex.EncodeToString(hash[:])
}

func (spider *Spider) startCollector() {
	spider.wg.Add(1)

	defer func() {
		spider.wg.Done()
		<-spider.sem
	}()

	c, err := spider.getCollector()

	if err != nil {
		spider.Logger.Error(err)
		return
	}

	ticker := time.NewTicker(time.Second)

	for {
		select {
		case <-spider.done:
			return
		case <-ticker.C:
			job, err := spider.JS.GetJob()
			if err == nil {
				spider.runningCollectors <- struct{}{}
				err := c.Visit(job.URL)
				if err != nil {
					spider.Logger.Debugf("Collector %d error: %s", c.ID, err.Error())
				} else {
					spider.Logger.Debugf("Collector %d started on %s", c.ID, job.URL)
				}
				c.Wait()
				if err == nil {
					spider.Logger.Debugf("Collector %d ended on %s", c.ID, job.URL)
				}
				<-spider.runningCollectors
			}
		}
	}
}

func (spider *Spider) startSeedCollector(url string) error {
	select {
	case spider.runningSeedCollectors <- struct{}{}:
		go func() {
			spider.wg.Add(1)
			c, err := spider.getSeedCollector()
			if err != nil {
				spider.Logger.Error(err)
				return
			}
			c.Visit(url)
			spider.Logger.Infof("Seed collector started on %s", url)
			c.Wait()
			spider.Logger.Infof("Seed collector on %s ended", url)
			spider.wg.Done()
			<-spider.runningSeedCollectors
		}()
		return nil
	default:
		return errors.New("Maximum number of seed collectors running, try later")
	}
}

func (spider *Spider) startPeriodicCollector(url string, interval time.Duration) {
	spider.wg.Add(1)
	defer spider.wg.Done()

	ticker := time.NewTicker(interval)

	for {
		select {
		case <-spider.done:
			return
		case <-ticker.C:
			c, err := spider.getSeedCollector()
			if err != nil {
				spider.Logger.Error(err)
				break
			}
			c.Visit(url)
			spider.Logger.Infof("Periodic collector on %s started", url)
			c.Wait()
			spider.Logger.Infof("Periodic collector on %s ended", url)

		}
	}
}

func (spider *Spider) AnalyzeSeeds() {

	c := colly.NewCollector()

	/*
		// Rotate two socks5 proxies
		rp, err := proxy.RoundRobinProxySwitcher("http://localhost:8119")
		if err != nil {
			log.Fatal(err)
		}
		c.SetProxyFunc(rp)
	*/

	c.WithTransport(&http.Transport{
		Proxy: http.ProxyURL(spider.ProxyURL),
		DialContext: (&net.Dialer{
			Timeout: 30 * time.Second,
			// KeepAlive: 30 * time.Second,
			DualStack: true,
		}).DialContext,
		MaxIdleConns:          100,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DisableKeepAlives:     true,
	})

	// create a request queue with 1 consumer thread until we solve the multi-threadin of the darknet model
	q, _ := queue.New(
		12,
		&queue.InMemoryQueueStorage{
			MaxSize: 1000000,
		},
	)

	c.OnError(func(r *colly.Response, err error) {
		fmt.Println("error:", err, r.Request.URL, r.StatusCode)
		var seedExists Seed
		spider.Rdbms.Where("url = ?", r.Request.URL.String()).First(&seedExists)
		seedExists.Visited = true
		seedExists.Status = r.StatusCode
		err = spider.Rdbms.Save(seedExists).Error
		if err != nil {
			spider.Logger.Fatal(err)
		}
	})

	c.OnHTML(`html`, func(e *colly.HTMLElement) {
		var seedExists Seed
		spider.Rdbms.Where("url = ?", e.Request.Ctx.Get("url")).First(&seedExists)
		// visisted
		seedExists.Visited = true
		// title
		seedExists.Title = e.ChildText("title")
		// description
		seedExists.Description = e.ChildText("meta[name=description]")
		// status
		seedExists.Status = 200
		err := spider.Rdbms.Save(seedExists).Error
		if err != nil {
			spider.Logger.Fatal(err)
		}
	})

	c.OnResponse(func(r *colly.Response) {
		fmt.Println("OnResponse from", r.Ctx.Get("url"))
		var seedExists Seed
		spider.Rdbms.Where("url = ?", r.Ctx.Get("url")).First(&seedExists)
		body := string(r.Body)
		bodyReader := strings.NewReader(body)
		dom, err := goquery.NewDocumentFromReader(bodyReader)
		if err != nil {
			spider.Logger.Error(err)
		}
		summary, err := articletext.GetArticleTextFromDocument(dom)
		if err != nil {
			spider.Logger.Error(err)
		}
		// summary
		seedExists.Summary = summary
		// extract a md5 hash of the text to avoid duplicate content (login pages, captachas,...)
		seedExists.Fingerprint = strToMD5(summary)

		err = spider.Rdbms.Save(seedExists).Error
		if err != nil {
			spider.Logger.Fatal(err)
		}
	})

	// Before making a request print "Visiting ..."
	c.OnRequest(func(r *colly.Request) {
		fmt.Println("Visiting", r.URL.String())
		r.Ctx.Put("url", r.URL.String())
	})

	var seeds []*Seed
	spider.Rdbms.Where("status IS NULL").Find(&seeds)
	for _, seed := range seeds {
		pp.Println(seed.URL)
		q.AddURL(seed.URL)
	}

	// Consume URLs
	q.Run(c)

}
