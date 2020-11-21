package internal

import (
	"encoding/json"
	"html/template"
	"time"

	"github.com/jinzhu/gorm"
	"github.com/jinzhu/now"
	"github.com/k0kubun/pp"
	"github.com/qor/admin"
)

type Chart struct {
	Total string
	Date  time.Time
}

/*
date format 2015-01-23
*/
func GetChartData(table, start, end string) (res []Chart) {
	startdate, err := now.Parse(start)
	if err != nil {
		return
	}

	enddate, err := now.Parse(end)
	if err != nil || enddate.UnixNano() < startdate.UnixNano() {
		enddate = now.EndOfDay()
	} else {
		enddate = enddate.AddDate(0, 0, 1)
	}

	GORMDB.Table(table).
		Where("created_at > ? AND created_at < ?", startdate, enddate).
		Select("date(created_at) as date, count(*) as total").
		Group("date(created_at)").
		Order("date(created_at)").
		Scan(&res)

	return
}

type Charts struct {
	Proxies []Chart
}

func ReportsDataHandler(context *admin.Context) {
	charts := &Charts{}
	startDate := context.Request.URL.Query().Get("startDate")
	endDate := context.Request.URL.Query().Get("endDate")

	pp.Println("startDate", startDate)
	pp.Println("endDate", endDate)

	charts.Proxies = GetChartData("proxies", startDate, endDate)
	pp.Println(charts)

	b, _ := json.Marshal(charts)
	context.Writer.Write(b)
	return
}

// SetupDashboard setup dashboard
func setupDashboard(db *gorm.DB, Admin *admin.Admin) {
	// hook database
	// DB = db
	// Add Dashboard
	Admin.AddMenu(&admin.Menu{Name: "Dashboard", Link: "/admin", Priority: 1})

	Admin.GetRouter().Get("/reports", ReportsDataHandler)
	initFuncMap(Admin)
}

func initFuncMap(Admin *admin.Admin) {
	Admin.RegisterFuncMap("render_latest_proxies", renderLatestPages)
}

func renderLatestPages(context *admin.Context) template.HTML {
	var pageContext = context.NewResourceContext("Proxy")
	pageContext.Searcher.Pagination.PerPage = 25
	if pages, err := pageContext.FindMany(); err == nil {
		return pageContext.Render("index/table", pages)
	}
	return template.HTML("")
}
