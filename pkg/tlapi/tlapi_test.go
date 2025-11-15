package tlapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestAPIHandlerSites(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("an error '%s' was not expected when opening a stub database connection", err)
	}
	defer db.Close()

	ts, err := time.Parse(time.RFC3339, "2006-01-02T15:04:05Z")
	if err != nil {
		t.Fatalf("unable to parse timestamp")
	}

	listRows := sqlmock.NewRows([]string{"id", "name", "ts"}).
		AddRow(1, "ABCD", ts)

	siteRows := sqlmock.NewRows([]string{"rank", "site"}).
		AddRow(1, "google.com").
		AddRow(2, "facebook.com").
		AddRow(3, "netflix.com").
		AddRow(4, "youtube.com").
		AddRow(5, "twitter.com").
		AddRow(6, "microsoft.com").
		AddRow(7, "amazon.com").
		AddRow(8, "tmall.com").
		AddRow(9, "linkedin.com").
		AddRow(10, "baidu.com")

	mock.ExpectBegin()
	mock.ExpectQuery("^SELECT id, name, ts FROM lists ORDER BY ts DESC LIMIT 1$").WillReturnRows(listRows)
	mock.ExpectQuery("^SELECT rank, site FROM sites WHERE list_id = \\$1 AND rank >= \\$2 ORDER BY rank ASC LIMIT \\$3$").WithArgs(1, 1, 10).WillReturnRows(siteRows)
	mock.ExpectRollback()

	config := newAPIServiceConfig()

	req, err := http.NewRequest("GET", "/api/sites", nil)
	if err != nil {
		t.Fatal(err)
	}

	// When API Path is "/" we need to exclude it in comparisons and
	// logs since otherwise that becomes "//sites" etc.
	basePath := ""
	if config.API.Path != "/" {
		basePath = config.API.Path
	}

	timeLoc, err := time.LoadLocation("GMT")
	if err != nil {
		t.Fatalf("unable to load GMT location")
	}

	// Create Recorder for saving the response.
	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(handlerWrapper(tlAPIHandler, db, config, timeLoc, basePath))

	// Do request
	handler.ServeHTTP(rr, req)

	// Check the status code is what we expect.
	if rr.Code != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v",
			rr.Code, http.StatusOK)
	}

	// Check the response body is what we expect.
	expected := `{"list":"ABCD","last-modified":"Mon, 02 Jan 2006 15:04:05 GMT","reference":"https://tranco-list.eu/list/ABCD","sites":[{"site":"google.com","rank":1},{"site":"facebook.com","rank":2},{"site":"netflix.com","rank":3},{"site":"youtube.com","rank":4},{"site":"twitter.com","rank":5},{"site":"microsoft.com","rank":6},{"site":"amazon.com","rank":7},{"site":"tmall.com","rank":8},{"site":"linkedin.com","rank":9},{"site":"baidu.com","rank":10}]}`
	expected = expected + "\n"
	if rr.Body.String() != expected {
		t.Errorf("handler returned unexpected body: got '%v' want '%v'",
			rr.Body.String(), expected)
	}
}

func TestAPIHandlerSitesWithQueryParams(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("an error '%s' was not expected when opening a stub database connection", err)
	}
	defer db.Close()

	ts, err := time.Parse(time.RFC3339, "2006-01-02T15:04:05Z")
	if err != nil {
		t.Fatalf("unable to parse timestamp")
	}

	listRows := sqlmock.NewRows([]string{"id", "name", "ts"}).
		AddRow(1, "ABCD", ts)

	siteRows := sqlmock.NewRows([]string{"rank", "site"}).
		AddRow(10, "baidu.com").
		AddRow(11, "instagram.com")

	mock.ExpectBegin()
	mock.ExpectQuery("^SELECT id, name, ts FROM lists ORDER BY ts DESC LIMIT 1$").WillReturnRows(listRows)
	mock.ExpectQuery("^SELECT rank, site FROM sites WHERE list_id = \\$1 AND rank >= \\$2 ORDER BY rank ASC LIMIT \\$3$").WithArgs(1, 10, 2).WillReturnRows(siteRows)
	mock.ExpectRollback()

	config := newAPIServiceConfig()

	req, err := http.NewRequest("GET", "/api/sites", nil)
	if err != nil {
		t.Fatal(err)
	}

	q := req.URL.Query()
	q.Add("start", "10")
	q.Add("count", "2")

	req.URL.RawQuery = q.Encode()

	// When API Path is "/" we need to exclude it in comparisons and
	// logs since otherwise that becomes "//sites" etc.
	basePath := ""
	if config.API.Path != "/" {
		basePath = config.API.Path
	}

	timeLoc, err := time.LoadLocation("GMT")
	if err != nil {
		t.Fatalf("unable to load GMT location")
	}

	// Create Recorder for saving the response.
	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(handlerWrapper(tlAPIHandler, db, config, timeLoc, basePath))

	// Do request
	handler.ServeHTTP(rr, req)

	// Check the status code is what we expect.
	if rr.Code != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v",
			rr.Code, http.StatusOK)
	}

	// Check the response body is what we expect.
	expected := `{"list":"ABCD","last-modified":"Mon, 02 Jan 2006 15:04:05 GMT","reference":"https://tranco-list.eu/list/ABCD","sites":[{"site":"baidu.com","rank":10},{"site":"instagram.com","rank":11}]}`
	expected = expected + "\n"
	if rr.Body.String() != expected {
		t.Errorf("handler returned unexpected body: got '%v' want '%v'",
			rr.Body.String(), expected)
	}
}

func TestAPIHandlerSite(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("an error '%s' was not expected when opening a stub database connection", err)
	}
	defer db.Close()

	ts, err := time.Parse(time.RFC3339, "2006-01-02T15:04:05Z")
	if err != nil {
		t.Fatalf("unable to parse timestamp")
	}

	listRows := sqlmock.NewRows([]string{"id", "name", "ts"}).
		AddRow(1, "ABCD", ts)

	rankRow := sqlmock.NewRows([]string{"rank"}).
		AddRow(1)

	mock.ExpectBegin()
	mock.ExpectQuery("^SELECT id, name, ts FROM lists ORDER BY ts DESC LIMIT 1$").WillReturnRows(listRows)
	mock.ExpectQuery("^SELECT rank FROM sites WHERE list_id = \\$1 AND site = \\$2$").WithArgs(1, "google.com").WillReturnRows(rankRow)
	mock.ExpectRollback()

	config := newAPIServiceConfig()

	req, err := http.NewRequest("GET", "/api/site/google.com", nil)
	if err != nil {
		t.Fatal(err)
	}

	// When API Path is "/" we need to exclude it in comparisons and
	// logs since otherwise that becomes "//sites" etc.
	basePath := ""
	if config.API.Path != "/" {
		basePath = config.API.Path
	}

	timeLoc, err := time.LoadLocation("GMT")
	if err != nil {
		t.Fatalf("unable to load GMT location")
	}

	// Create Recorder for saving the response.
	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(handlerWrapper(tlAPIHandler, db, config, timeLoc, basePath))

	// Do request
	handler.ServeHTTP(rr, req)

	// Check the status code is what we expect.
	if rr.Code != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v",
			rr.Code, http.StatusOK)
	}

	// Check the response body is what we expect.
	expected := `{"list":"ABCD","last-modified":"Mon, 02 Jan 2006 15:04:05 GMT","reference":"https://tranco-list.eu/list/ABCD","sites":[{"site":"google.com","rank":1}]}`
	expected = expected + "\n"
	if rr.Body.String() != expected {
		t.Errorf("handler returned unexpected body: got '%v' want '%v'",
			rr.Body.String(), expected)
	}
}

func TestAPIHandlerRank(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("an error '%s' was not expected when opening a stub database connection", err)
	}
	defer db.Close()

	ts, err := time.Parse(time.RFC3339, "2006-01-02T15:04:05Z")
	if err != nil {
		t.Fatalf("unable to parse timestamp")
	}

	listRows := sqlmock.NewRows([]string{"id", "name", "ts"}).
		AddRow(1, "ABCD", ts)

	siteRow := sqlmock.NewRows([]string{"site"}).
		AddRow("google.com")

	mock.ExpectBegin()
	mock.ExpectQuery("^SELECT id, name, ts FROM lists ORDER BY ts DESC LIMIT 1$").WillReturnRows(listRows)
	mock.ExpectQuery("^SELECT site FROM sites WHERE list_id = \\$1 AND rank = \\$2$").WithArgs(1, 1).WillReturnRows(siteRow)
	mock.ExpectRollback()

	config := newAPIServiceConfig()

	req, err := http.NewRequest("GET", "/api/rank/1", nil)
	if err != nil {
		t.Fatal(err)
	}

	// When API Path is "/" we need to exclude it in comparisons and
	// logs since otherwise that becomes "//sites" etc.
	basePath := ""
	if config.API.Path != "/" {
		basePath = config.API.Path
	}

	timeLoc, err := time.LoadLocation("GMT")
	if err != nil {
		t.Fatalf("unable to load GMT location")
	}

	// Create Recorder for saving the response.
	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(handlerWrapper(tlAPIHandler, db, config, timeLoc, basePath))

	// Do request
	handler.ServeHTTP(rr, req)

	// Check the status code is what we expect.
	if rr.Code != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v",
			rr.Code, http.StatusOK)
	}

	// Check the response body is what we expect.
	expected := `{"list":"ABCD","last-modified":"Mon, 02 Jan 2006 15:04:05 GMT","reference":"https://tranco-list.eu/list/ABCD","sites":[{"site":"google.com","rank":1}]}`
	expected = expected + "\n"
	if rr.Body.String() != expected {
		t.Errorf("handler returned unexpected body: got '%v' want '%v'",
			rr.Body.String(), expected)
	}
}
