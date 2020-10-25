# tranco-list-api

**This is an early work in progress, anything from database schema, JSON format or API endpoints are candidates for change**

This is code that can be used to create a read-only HTTP API for the [Tranco](https://tranco-list.eu/) top
one million sites list.

The parts involved:
* PostgreSQL is expected as the storage backend.
* `tldbwriter` continously checks for the latest list ID and loads it into the database.
* `tlapid` responds to HTTP requests based on the database contents.

## Building containers (substitute "eest" with your own account or registry)
$ docker build -t eest/tldbwriter:vX.Y.Z -f Dockerfile-tldbwriter .
$ docker push eest/tldbwriter:vX.Y.Z

$ docker build -t eest/tlapid:vX.Y.Z -f Dockerfile-tlapid .
$ docker push eest/tlapid:vX.Y.Z

## Currently supported endpoints

### Get the top ten sites
```
$ curl -s https://example.com/api/sites | jq .
{
  "list": "66NX",
  "last-modified": "Fri, 06 Sep 2019 22:15:50 GMT",
  "reference": "https://tranco-list.eu/list/66NX",
  "sites": [
    {
      "site": "google.com",
      "rank": 1
    },
    {
      "site": "facebook.com",
      "rank": 2
    },
    {
      "site": "netflix.com",
      "rank": 3
    },
    {
      "site": "youtube.com",
      "rank": 4
    },
    {
      "site": "microsoft.com",
      "rank": 5
    },
    {
      "site": "amazon.com",
      "rank": 6
    },
    {
      "site": "twitter.com",
      "rank": 7
    },
    {
      "site": "tmall.com",
      "rank": 8
    },
    {
      "site": "instagram.com",
      "rank": 9
    },
    {
      "site": "linkedin.com",
      "rank": 10
    }
  ]
}
```

### Set a starting point and/or number of results to get
```
$ curl -s 'https://example.com/api/sites?start=50&count=2' | jq .
{
  "list": "66NX",
  "last-modified": "Fri, 06 Sep 2019 22:15:50 GMT",
  "reference": "https://tranco-list.eu/list/66NX",
  "sites": [
    {
      "site": "mozilla.org",
      "rank": 50
    },
    {
      "site": "googleusercontent.com",
      "rank": 51
    }
  ]
}
```

### Fetch a site by name
```
$ curl -s https://example.com/api/site/google.com | jq .
{
  "list": "66NX",
  "last-modified": "Fri, 06 Sep 2019 22:15:50 GMT",
  "reference": "https://tranco-list.eu/list/66NX",
  "sites": [
    {
      "site": "google.com",
      "rank": 1
    }
  ]
}
```

### Fetch a site by rank
```
$ curl -s https://example.com/api/rank/1 | jq .
{
  "list": "66NX",
  "last-modified": "Fri, 06 Sep 2019 22:15:50 GMT",
  "reference": "https://tranco-list.eu/list/66NX",
  "sites": [
    {
      "site": "google.com",
      "rank": 1
    }
  ]
}
```
