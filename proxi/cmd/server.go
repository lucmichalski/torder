/*
Copyright © 2020 nicksherron <nsherron90@gmail.com>

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package cmd

import (
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"runtime/trace"
	"strconv"
	"syscall"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/nicksherron/proxi/internal"
	"github.com/spf13/cobra"
)

var (
	updateFreq        int
	downloadCheckInit bool
	checkInit         bool
	traceProfile      string
	cpuProfile        string
	memProfile        string
	pingDB            bool
	serverCmd         = &cobra.Command{
		Use:   "server",
		Short: "Download then check proxies and start rest api server for querying results.",
		Run: func(cmd *cobra.Command, args []string) {
			cmd.Flags().Parse(args)
			internal.DbInit()
			oldLimit, newLimit := internal.IncrFdLimit()
			if newLimit != 0 {
				log.Printf("Increased maximum number of open files to %v (it was originally set to %v).",
					newLimit, oldLimit)
			}
			if pingDB {
				internal.DbPing()
				return
			}
			if cpuProfile != "" || memProfile != "" || traceProfile != "" {
				profileInit()
			}
			internal.StartupMessage()
			go schedule()
			if downloadCheckInit {
				time.Sleep(10 * time.Millisecond)
				go internal.DownloadInit()
			} else if checkInit {
				time.Sleep(10 * time.Millisecond)
				go internal.CheckInit()
			}
			internal.API()
		},
	}
)

func init() {
	rootCmd.AddCommand(serverCmd)
	serverCmd.PersistentFlags().StringVar(&traceProfile, "trace", "", "Write trace profile to file.")
	serverCmd.PersistentFlags().StringVar(&cpuProfile, "cpu", "", "Write cpu profile to file.")
	serverCmd.PersistentFlags().StringVar(&memProfile, "mem", "", "Write memory profile to file.")
	serverCmd.PersistentFlags().BoolVar(&downloadCheckInit, "init", false, "Initialize proxy download and check process after server start.")
	serverCmd.PersistentFlags().BoolVar(&checkInit, "check", false, "Initialize proxy  check process after server start.")
	serverCmd.PersistentFlags().StringVarP(&internal.Addr, "addr", "a", listenAddr(), "Ip and port to listen and serve on.")
	serverCmd.PersistentFlags().StringVar(&internal.MaxmindFilePath, "maxmind-file", maxmindPath(), "Maxmind country db file. Downloads if default doesn't exist.")
	serverCmd.PersistentFlags().StringVar(&internal.Driver, "driver", "sqlite", "Driver for the backend storage (Available mysql, postgres and sqlite).")
	serverCmd.PersistentFlags().StringVar(&internal.DbPath, "db", dbPath(), "Sqlite3 backend storage file location.")
	serverCmd.PersistentFlags().StringVar(&internal.LogFile, "log", logPath(), "Set filepath for HTTP log.")
	serverCmd.PersistentFlags().IntVar(&internal.FileLimitMax, "ulimit", 2048, "Number of allowed file handles per process.")
	serverCmd.PersistentFlags().IntVar(&updateFreq, "interval", 12, "Wait interval in hours before (re)checking proxies and downloading new ones.")
	serverCmd.PersistentFlags().DurationVar(&internal.Timeout, "check-timeout", 30*time.Second, "Specify request time out for checking proxies.")
	serverCmd.PersistentFlags().DurationVar(&internal.DownloadTimeout, "download-timeout", 60*time.Second, "Specify timeout out for downloading proxies.")
	serverCmd.PersistentFlags().IntVarP(&internal.Workers, "workers", "w", workerN(), "Number of (goroutines) concurrent requests to make for checking proxies.")
	serverCmd.PersistentFlags().BoolVar(&pingDB, "ping", false, "Ping db and exit.")
	serverCmd.PersistentFlags().BoolVarP(&internal.Progress, "progress", "p", isTerminal(os.Stderr), "Show proxy test progress bar.")
}

func listenAddr() string {
	var a string
	if os.Getenv("PROXI_ADDRESS") != "" {
		a = os.Getenv("PROXI_ADDRESS")
		return a
	}
	a = "0.0.0.0:4444"
	return a

}

func workerN() int {
	if os.Getenv("PROXI_WORKERS") != "" {
		s := os.Getenv("PROXI_WORKERS")
		if w, ok := strconv.Atoi(s); ok == nil {
			return w
		} else {
			w = 100
			return w
		}
	}
	w := 100
	return w

}

func maxmindPath() string {
	maxmindFile := "GeoLite2-Country.mmdb"
	f := filepath.Join(dataHome(), maxmindFile)
	return f
}

func dbPath() string {
	dbFile := "data.db"
	f := filepath.Join(dataHome(), dbFile)
	return f
}

func logPath() string {
	logFile := "server.log"
	f := filepath.Join(configHome(), logFile)
	return f
}

func profileInit() {

	go func() {
		defer os.Exit(1)
		if traceProfile != "" {
			f, err := os.Create(traceProfile)
			if err != nil {
				log.Fatal("could not create trace profile: ", err)
			}
			defer f.Close()
			if err := trace.Start(f); err != nil {
				log.Fatal("could not start trace profile: ", err)
			}
			defer trace.Stop()
		}

		if cpuProfile != "" {
			f, err := os.Create(cpuProfile)
			if err != nil {
				log.Fatal("could not create CPU profile: ", err)
			}
			defer f.Close()
			if err := pprof.StartCPUProfile(f); err != nil {
				log.Fatal("could not start CPU profile: ", err)
			}
			defer pprof.StopCPUProfile()
		}

		defer func() {
			if memProfile != "" {
				mf, err := os.Create(memProfile)
				if err != nil {
					log.Fatal("could not create memory profile: ", err)
				}
				defer mf.Close()
				runtime.GC() // get up-to-date statistics
				if err := pprof.WriteHeapProfile(mf); err != nil {
					log.Fatal("could not write memory profile: ", err)
				}
			}
		}()

		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
		<-sigs
	}()
}

func isTerminal(f *os.File) bool {
	if runtime.GOOS == "windows" {
		return false
	}

	fd := f.Fd()
	return os.Getenv("TERM") != "dumb" && (isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd))
}

func schedule() {
	for {
		sleepDur := time.Duration(updateFreq) * time.Hour
		next := time.Now().Add(sleepDur)
		log.Println("Next proxy download and check scheduled for ", next)
		time.Sleep(sleepDur)
		internal.DownloadInit()
	}

}
