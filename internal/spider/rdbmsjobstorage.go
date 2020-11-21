package spider

/*
import (
	"sync"
	"time"

	"github.com/jinzhu/gorm"
	"github.com/qor/media"
	"github.com/qor/validations"
	log "github.com/sirupsen/logrus"
)

// RdbmsJobsStorage is an implementation of the JobsStorage interface
type RdbmsJobsStorage struct {
	DatabaseName   string
	CollectionName string
	Logger         *log.Logger
	URI            string
	BufferSize     int

	jobs    chan Job
	done    chan struct{}
	filling chan struct{}
	wg      *sync.WaitGroup
	min     int

	client *gorm.DB
}

// NewRdbmsJobsStorage returns an instance
func NewRdbmsJobsStorage(driver string, database string, user string, password string, host string, port string) (*RdbmsJobsStorage, error) {
	s := &RdbmsJobsStorage{
		URI:            URI,
		DatabaseName:   dbName,
		CollectionName: colName,
		jobs:           make(chan Job, bufSize),
	}

	s.done = make(chan struct{})
	s.filling = make(chan struct{}, 1)
	s.min = min
	s.wg = &sync.WaitGroup{}

	db, err := gorm.Open("mysql", fmt.Sprintf("%v:%v@tcp(%v:%v)/%v?charset=utf8mb4,utf8&parseTime=True", os.Getenv("TOR_MYSQL_USER"), os.Getenv("TOR_MYSQL_PASSWORD"), os.Getenv("TOR_MYSQL_HOST"), os.Getenv("TOR_MYSQL_PORT"), os.Getenv("TOR_MYSQL_DATABASE")))
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// callback for images and validation
	validations.RegisterCallbacks(db)
	media.RegisterCallbacks(db)

	return s, nil
}

func NewRdbmsJobsFromExisting(db *gorm.DB) *RdbmsJobsStorage {
	s := &RdbmsJobsStorage{
		client: db,
		jobs:   make(chan Job, bufSize),
	}
	s.done = make(chan struct{})
	s.filling = make(chan struct{}, 1)
	s.min = min
	s.wg = &sync.WaitGroup{}
	return s
}

// Start checks one time at a second if there are enough jobs in the channel, and if there are not it fetches them from the database
func (s *RdbmsJobsStorage) Start() {
	s.wg.Add(1)
	defer s.wg.Done()
	ticker := time.NewTicker(time.Second)
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			if len(s.jobs) < s.min {
				go s.fillJobsChannel()
			}
		}

	}
}

// Stop signals to the getter and the saver to stop
func (s *RdbmsJobsStorage) Stop() error {
	// TODO close jobs channel?
	close(s.done)
	s.wg.Wait()
	s.db.Close()
	return nil
}

// Status returns the status
func (s *RdbmsJobsStorage) Status() string {
	var b strings.Builder
	b.Grow(40)

	select {
	case <-s.done:
		fmt.Fprint(&b, "Stopped. ")
	default:
		fmt.Fprint(&b, "Running. ")
	}
	fmt.Fprintf(&b, "There are %d cached jobs ", len(s.jobs))
	count, err := s.countJobsInDb()

	if err != nil {
		fmt.Fprintf(&b, "and db is unreachable %v", err)
	} else {
		fmt.Fprintf(&b, "and %d jobs in the db", count)
	}

	return b.String()
}

// GetJob returns a job if it's cached in the jobs channel, otherwise, it caches
// jobs from the database if there is no other goroutine doing it. It timeouts
// after 3 seconds.
func (s *RdbmsJobsStorage) GetJob() (Job, error) {
	select {
	case job := <-s.jobs:
		return job, nil
	case <-time.After(3 * time.Second):
		return Job{}, &NoJobsError{"No jobs"}
	}
}

// SaveJob adds a job to the channel if it's not full, otherwise it flushes the
// channel to the db and then adds the job to the channel.
func (s *RdbmsJobsStorage) SaveJob(job Job) {
	select {
	case s.jobs <- job:
		return
	default:
		n := len(s.jobs) - s.min
		s.flush(n)
		s.jobs <- job
		select {
		case s.jobs <- job:
		case <-time.After(3 * time.Second):
			s.Logger.Errorf("Cannot save Job %v", job)
		}
	}
}

func (s *RdbmsJobsStorage) deleteJobsFromDb(jobs []string) (int64, error) {
	deletedCount := 0
	for _, job := range jobs {
		err := db.Delete(Job{}, "url = ?", job.URL).Error
		if err != nil {
			return 0, err
		}
		deletedCount++
	}
	return deletedCount, nil
}

func (s *RdbmsJobsStorage) fillJobsChannel() {
	s.wg.Add(1)
	defer s.wg.Done()

	select {
	case s.filling <- struct{}{}:
		defer func() {
			<-s.filling
		}()
		s.Logger.Debug("Started filler")

		cur, err := s.getCursor()

		if err != nil {
			s.Logger.Error(err)
			return
		}

		defer cur.Close(context.Background())

		jobs := make([]string, 0)

	loop:
		for cur.Next(context.Background()) {
			var job Job
			err := cur.Decode(&job)
			if err != nil {
				s.Logger.Error(err)
			} else {
				select {
				case s.jobs <- job:
					jobs = append(jobs, job.URL)
				default:
					break loop
				}
			}
		}

		if err := cur.Err(); err != nil {
			log.Error(err)
		}

		deletedCount, err := s.deleteJobsFromDb(jobs)

		if err != nil {
			s.Logger.Error(err)
			return
		}

		s.Logger.Debugf("Got %d jobs, deleted %d jobs", len(jobs), deletedCount)
	}
}

func (s *RdbmsJobsStorage) flush(max int) (int, error) {
	jobs := make([]interface{}, 0)
loop:
	for i := 0; i < max; i++ {
		select {
		case job := <-s.jobs:
			jobs = append(jobs, job)
		default:
			break loop
		}
	}

	if len(jobs) == 0 {
		return 0, nil
	}

	count := 0
	for _, job := range jobs {
		err := s.rdbmds.Create(&Job{URL: job}).Error
		if err != nil {
			return count, err
		}
		count++
	}

	if count != len(jobs) {
		s.Logger.Errorf("%d jobs were not saved", len(jobs)-count)
	}
	return count, nil
}

func (s *RdbmsJobsStorage) countJobsInDb() (int64, error) {
	var count int64
	err := s.rdbms.Model(&Job{}).Count(&count).Error
	if err != nil {
		return 0, err
	}
	return count, nil
}
*/
