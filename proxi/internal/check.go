/*
 * Copyright © 2020 nicksherron <nsherron90@gmail.com>
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package internal

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cheggaaa/pb/v3"
	"github.com/nozzle/throttler"
	"github.com/olekukonko/tablewriter"
)

type httpBin struct {
	Origin string `json:"origin"`
}

var (
	checkedProxies Proxies
	counter        int64
	realIP         string
	wgDB           sync.WaitGroup
	wgC            sync.WaitGroup
	wgLoop         sync.WaitGroup
	// Workers controls number of max goroutines at a time for checking proxies.
	Workers int
	// Timeout sets http request timeouts for proxy checks
	Timeout time.Duration
	// Progress determines if we use progress bar when checking proxies.
	Progress     bool
	testCount    int64
	bar          *pb.ProgressBar
	barTemplate  = `{{string . "message"}}{{counters . }} {{bar . }} {{percent . }} {{speed . "%s req/sec" }}`
	judgeUrl     string
	resolveCount int
)

func resolveJudges() {

	suffix := "/get?show_env"
	sites := []string{
		"http://httpbin.org",
		"http://httpbin.net",
		"http://eu.httpbin.org",
	}
	if os.Getenv("PROXI_JUDGES") != "" {
		for _, v := range strings.Split(os.Getenv("PROXI_JUDGES"), `,`) {
			sites = append(sites, strings.TrimSpace(v))
		}

	}

	var sources = struct {
		sync.RWMutex
		counter map[string]int
		timer   map[string]time.Duration
	}{counter: make(map[string]int), timer: make(map[string]time.Duration)}

	var limit int

	if Workers < 100 {
		limit = Workers
	} else {
		limit = 100
	}

	//	make n requests  with each url and choose whichever one gets done the fastest
	for _, site := range sites {
		u := site + suffix
		var w sync.WaitGroup
		start := time.Now()
		for i := 0; i < limit; i++ {
			w.Add(1)
			go func() {
				defer w.Done()
				req, err := http.NewRequest("GET", u, nil)
				if err != nil {
					return
				}
				curl := &http.Client{}
				resp, err := curl.Do(req)
				if err != nil {
					return
				}
				defer resp.Body.Close()
				body, err := ioutil.ReadAll(resp.Body)
				if err != nil {
					return
				}
				var jsonBody httpBin
				err = json.Unmarshal(body, &jsonBody)
				if jsonBody.Origin != "" {
					sources.Lock()
					sources.counter[site]++
					sources.Unlock()
				}
			}()
		}
		w.Wait()
		sources.timer[site] = time.Since(start)
	}

	type rec struct {
		Key   string
		Value int
	}
	var records []rec
	for k, v := range sources.timer {
		if float64(sources.counter[k]/limit) > .80 {
			records = append(records, rec{k, int(v)})
		}
	}
	resolveCount++
	if len(records) == 0 {
		if resolveCount < 3 {
			log.Printf("Can't connect to test sites. Trying again, attempt %v\n", resolveCount)
			time.Sleep(5 * time.Second)
			resolveJudges()
			return
		} else {
			log.Fatal("Something went wrong trying to connect to test sites.")
		}
	}

	sort.Slice(records, func(i, j int) bool {
		return records[i].Value < records[j].Value
	})

	if os.Getenv("PROXI_DEBUG_JUDGES") == "1" {
		table := tablewriter.NewWriter(os.Stdout)
		table.SetHeader([]string{"success_tests", "response_time", "site"})
		for _, v := range sites {
			c := strconv.Itoa(sources.counter[v])
			table.Append([]string{c, sources.timer[v].String(), v})
		}
		fmt.Println()
		table.Render()
	}
	judgeUrl = records[0].Key + suffix

}

func hostIP() string {
	req, err := http.NewRequest("GET", judgeUrl, nil)
	check(err)
	curl := &http.Client{}
	resp, err := curl.Do(req)
	check(err)
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	check(err)
	var jsonBody httpBin
	err = json.Unmarshal(body, &jsonBody)
	return jsonBody.Origin
}

func proxyCheck(proxy *Proxy) {

	proxy.CheckCount++

	defer func() {
		wgC.Done()
		if Progress {
			bar.Add(1)
		}
		atomic.AddInt64(&testCount, 1)
		if r := recover(); r != nil {
			proxy.LosingStreak++
			if strings.Contains(fmt.Sprintf("%v", r), "Client.Timeout exceeded while awaiting headers") {
				proxy.LastStatus = "timeout"
				proxy.TimeoutCount++
				mutex.Lock()
				checkedProxies = append(checkedProxies, proxy)
				mutex.Unlock()
				return
			}
			proxy.LastStatus = "fail"
			proxy.FailCount++
			mutex.Lock()
			checkedProxies = append(checkedProxies, proxy)
			mutex.Unlock()
			return
		}
	}()

	if proxy.LosingStreak >= 5 {
		proxy.Deleted = true
		mutex.Lock()
		checkedProxies = append(checkedProxies, proxy)
		mutex.Unlock()
		return
	}

	if proxy.CheckCount > 5 {
		if float64(proxy.FailCount)/float64(proxy.CheckCount) >= 0.90 {
			proxy.Deleted = true
			mutex.Lock()
			checkedProxies = append(checkedProxies, proxy)
			mutex.Unlock()
			return
		}
	}
	if proxy.CheckCount > 10 {
		if float64(proxy.FailCount)/float64(proxy.CheckCount) >= 0.80 {
			proxy.Deleted = true
			mutex.Lock()
			checkedProxies = append(checkedProxies, proxy)
			mutex.Unlock()
			return
		}
	}
	prox := proxy.Proxy
	proxyURL, err := url.Parse(prox)
	check(err)

	tr := &http.Transport{
		Proxy:               http.ProxyURL(proxyURL),
		TLSHandshakeTimeout: 60 * time.Second,
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{
		Timeout:   Timeout,
		Transport: tr,
	}

	proxy.Judge = judgeUrl

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), Timeout+5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", judgeUrl, nil)
	check(err)
	req.Header.Set("User-Agent", `Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_1) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/78.0.3904.108 Safari/537.36`)
	resp, err := client.Do(req)
	if err != nil {
		check(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return
	}
	body, err := ioutil.ReadAll(resp.Body)
	check(err)

	*proxy.RespTime = time.Since(start).Truncate(time.Millisecond).String()

	var jsonBody httpBin
	err = json.Unmarshal(body, &jsonBody)
	check(err)

	if jsonBody.Origin == "" {
		return
	}

	if strings.Contains(jsonBody.Origin, realIP) {
		proxy.Anonymous = false
	} else {
		proxy.Anonymous = true
	}

	proxy.LastStatus = "good"
	proxy.LosingStreak = 0
	proxy.SuccessCount++
	mutex.Lock()
	checkedProxies = append(checkedProxies, proxy)
	mutex.Unlock()

}

// CheckInit checks all proxies from GormDB to see if they are transparent or anonymous and if they work.
func CheckInit() {
	log.Println("Starting proxy checks...")
	busy = true
	resolveJudges()
	if os.Getenv("PROXI_DEBUG_JUDGES") == "1" {
		fmt.Println(judgeUrl)
	}
	var proxies Proxies
	proxies = dbFind()

	var limit int64

	if Workers > len(proxies) {
		limit = int64(len(proxies))
	} else {
		limit = int64(Workers)
	}
	log.SetOutput(nil)
	atomic.StoreInt64(&testCount, 0)
	realIP = hostIP()
	counter = 0

	t := throttler.New(100, len(proxies))

	if Progress {
		bar = pb.ProgressBarTemplate(barTemplate).Start(len(proxies)).SetMaxWidth(60)
		bar.Set("message", "Testing proxies\t")
	}

	for _, proxy := range proxies {
		go func(p *Proxy) error {
			defer t.Done(nil)

			atomic.AddInt64(&counter, 1)
			proxyCheck(p)
			if atomic.CompareAndSwapInt64(&counter, limit, 0) {
				storeCheckedProxies()
			}

			return nil
		}(proxy)

		t.Throttle()

	}

	// throttler errors iteration
	if t.Err() != nil {
		// Loop through the errors to see the details
		for i, err := range t.Errs() {
			log.Printf("error #%d: %s", i, err)
		}
		log.Fatal(t.Err())
	}

	if Progress {
		bar.Finish()
	}
	log.SetOutput(os.Stderr)
	log.Println("Done checking proxies.")
	busy = false

	/*
		wgLoop.Add(1)
		if Progress {
			bar = pb.ProgressBarTemplate(barTemplate).Start(len(proxies)).SetMaxWidth(60)
			bar.Set("message", "Testing proxies\t")
		}
		go func() {
			defer wgLoop.Done()
			for _, proxy := range proxies {
				wgC.Add(1)
				atomic.AddInt64(&counter, 1)
				go proxyCheck(proxy)
				if atomic.CompareAndSwapInt64(&counter, limit, 0) {
					wgC.Wait()
					wgDB.Add(1)
					go storeCheckedProxies()

				}
			}
		}()
		wgLoop.Wait()
		if counter > 0 {
			wgC.Wait()
		}
		wgDB.Add(1)
		go storeCheckedProxies()
		wgDB.Wait()

		if Progress {
			bar.Finish()
		}
		log.SetOutput(os.Stderr)
		log.Println("Done checking proxies.")
		busy = false
	*/
}

func storeCheckedProxies() {
	defer wgDB.Done()
	dbPrepWrite()
	mutex.Lock()
	proxies := checkedProxies
	checkedProxies = Proxies{}
	mutex.Unlock()
	for _, proxy := range proxies {
		dbInsert(proxy)
	}
}
