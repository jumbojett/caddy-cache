package cache

import (
	"fmt"
	"github.com/stretchr/testify/assert"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

/* Helpers */

type TestHandler struct {
	maxConcurrencyLevel     int
	currentConcurrencyLevel int
	StatsLock               *sync.Mutex
	Latency                 time.Duration
	TimeToEnd               time.Duration
	timesCalled             int
	ResponseBody            []byte
	ResponseCode            int
	ResponseError           error
	ResponseHeaders         http.Header
}

func (h *TestHandler) TimesCalled() int {
	h.StatsLock.Lock()
	defer h.StatsLock.Unlock()
	return h.timesCalled
}

func (h *TestHandler) ConcurrencyLevel() int {
	h.StatsLock.Lock()
	defer h.StatsLock.Unlock()
	return h.currentConcurrencyLevel
}

func (h *TestHandler) MaxConcurrencyLevel() int {
	h.StatsLock.Lock()
	defer h.StatsLock.Unlock()
	return h.maxConcurrencyLevel
}

func (h *TestHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) (int, error) {
	// Update concurrency stats
	h.StatsLock.Lock()
	h.timesCalled++
	h.currentConcurrencyLevel++
	if h.currentConcurrencyLevel > h.maxConcurrencyLevel {
		h.maxConcurrencyLevel = h.currentConcurrencyLevel
	}
	h.StatsLock.Unlock()
	defer func() {
		h.StatsLock.Lock()
		h.currentConcurrencyLevel--
		h.StatsLock.Unlock()
	}()

	time.Sleep(h.Latency)

	for k, values := range h.ResponseHeaders {
		for _, v := range values {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(h.ResponseCode)

	if h.ResponseBody != nil && r.Method != "HEAD" {
		w.Write(h.ResponseBody)
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	time.Sleep(h.TimeToEnd)
	return h.ResponseCode, h.ResponseError
}

func buildHandlerWithCache(cache *Cache) (*CacheHandler, *TestHandler) {
	backend := TestHandler{
		ResponseBody: []byte("Hello :)"),
		StatsLock:    new(sync.Mutex),
		Latency:        0,
		ResponseCode: 200,
	}

	return &CacheHandler{
		Config: &Config{
			CacheRules:    []CacheRule{},
			DefaultMaxAge: time.Duration(10) * time.Second,
		},
		Cache: cache,
		Next:  &backend,
	}, &backend
}

func buildBasicHandler() (*CacheHandler, *TestHandler) {
	cache := NewCache(NewMemoryStorage())
	cache.Setup()
	return buildHandlerWithCache(cache)
}

func buildRequest(path string, method string, headers http.Header) *http.Request {
	reqUrl, err := url.Parse(path)
	if err != nil {
		panic(fmt.Sprintf("Invalid url %s in test", path))
	}
	return &http.Request{
		Method: method,
		URL:    reqUrl,
		Header: headers,
	}
}

func buildGetRequest(path string) *http.Request {
	return buildRequest(path, "GET", http.Header{})
}

func makeNRequests(handler *CacheHandler, n int, req *http.Request) []*http.Response {
	responses := []*http.Response{}
	for i := 0; i < n; i++ {
		recorder := httptest.NewRecorder()
		_, err := handler.ServeHTTP(recorder, req)
		if err != nil {
			panic(err)
		}
		responses = append(responses, recorder.Result())
	}
	return responses
}

type ConcurrentResult struct {
	result *http.Response
	err    error
}

func makeNConcurrentRequests(handler *CacheHandler, n int, req *http.Request) ([]*http.Response, error) {
	channel := make(chan ConcurrentResult)
	for i := 0; i < n; i++ {
		go func(channel chan ConcurrentResult) {
			recorder := httptest.NewRecorder()
			_, err := handler.ServeHTTP(recorder, req)
			if err != nil {
				channel <- ConcurrentResult{
					err: err,
				}
			}
			channel <- ConcurrentResult{
				result: recorder.Result(),
			}
		}(channel)
	}

	responses := []*http.Response{}
	for i := 0; i < n; i++ {
		res := <-channel
		if res.err != nil {
			panic(res.err)
		}
		responses = append(responses, res.result)
	}
	return responses, nil
}

/* Actual Tests */

// TODO avoid code duplication, use r.Run

func TestCacheByCacheControlHeader(t *testing.T) {
	handler, backend := buildBasicHandler()
	backend.ResponseHeaders = http.Header{
		"Cache-control": []string{"public; max-age=3600"},
	}

	makeNRequests(handler, 2, buildGetRequest("http://somehost.com/"))
	assert.Equal(t, 1, backend.timesCalled, "Backend should have been called 1 but it was called", backend.timesCalled)
}

func TestCacheByExpiresHeader(t *testing.T) {
	handler, backend := buildBasicHandler()
	backend.ResponseHeaders = http.Header{
		"Expires": []string{"Thu, 01 Dec 2820 16:00:00 GMT"},
	}

	makeNRequests(handler, 2, buildGetRequest("http://somehost.com/"))
	assert.Equal(t, 1, backend.timesCalled, "Backend should have been called 1 but it was called", backend.timesCalled)
}

func TestNoCacheByExpiredHeader(t *testing.T) {
	handler, backend := buildBasicHandler()
	backend.ResponseHeaders = http.Header{
		"Expires": []string{"Thu, 01 Dec 1994 16:00:00 GMT"},
	}

	makeNRequests(handler, 2, buildGetRequest("http://somehost.com/"))
	assert.Equal(t, 2, backend.timesCalled, "Backend should have been called 1 but it was called", backend.timesCalled)
}

func TestCacheByPath(t *testing.T) {
	handler, backend := buildBasicHandler()
	handler.Config.CacheRules = append(handler.Config.CacheRules, &PathCacheRule{Path: "/assets"})

	req := buildGetRequest("http://somehost.com/assets/1")

	makeNRequests(handler, 2, req)
	assert.Equal(t, 1, backend.timesCalled, "Backend should have been called 1 but it was called", backend.timesCalled)
}

func TestNotCacheablePath(t *testing.T) {
	handler, backend := buildBasicHandler()
	handler.Config.CacheRules = append(handler.Config.CacheRules, &PathCacheRule{Path: "/assets"})

	req := buildGetRequest("http://somehost.com/api/1")

	makeNRequests(handler, 2, req)
	assert.Equal(t, 2, backend.timesCalled, "Backend should have been called 2 but it was called", backend.timesCalled)
}

func TestNotCacheableMethod(t *testing.T) {
	handler, backend := buildBasicHandler()
	handler.Config.CacheRules = append(handler.Config.CacheRules, &PathCacheRule{Path: "/assets"})

	makeNRequests(handler, 2, buildRequest("http://somehost.com/assets/1", "POST", http.Header{}))
	assert.Equal(t, 2, backend.timesCalled, "Backend should have been called 2 but it was called", backend.timesCalled)
}

func TestCacheableHead(t *testing.T) {
	handler, backend := buildBasicHandler()
	handler.Config.CacheRules = append(handler.Config.CacheRules, &PathCacheRule{Path: "/assets"})
	backend.ResponseBody = nil

	makeNRequests(handler, 2, buildRequest("http://somehost.com/assets/1", "HEAD", http.Header{}))
	assert.Equal(t, 1, backend.timesCalled, "Backend should have been called 1 but it was called", backend.timesCalled)
}

func TestNonCacheableHead(t *testing.T) {
	handler, backend := buildBasicHandler()
	handler.Config.CacheRules = append(handler.Config.CacheRules, &PathCacheRule{Path: "/assets"})
	backend.ResponseBody = nil

	makeNRequests(handler, 2, buildRequest("http://somehost.com/", "HEAD", http.Header{}))
	assert.Equal(t, 2, backend.timesCalled, "Backend should have been called 2 but it was called", backend.timesCalled)
}

func TestNotCacheableCacheControl(t *testing.T) {
	handler, backend := buildBasicHandler()
	handler.Config.CacheRules = append(handler.Config.CacheRules, &PathCacheRule{Path: "/assets"})

	backend.ResponseHeaders = http.Header{
		"Cache-control": []string{"private"},
	}

	makeNRequests(handler, 2, buildGetRequest("http://somehost.com/assets/1"))
	assert.Equal(t, 2, backend.timesCalled, "Backend should have been called 2 but it was called", backend.timesCalled)
}

func TestAddAllHeaders(t *testing.T) {
	handler, backend := buildBasicHandler()

	responseHeaders := http.Header{
		"Content-Type": []string{"text/plain; charset=utf-8"},
		"X-Custom-2":   []string{"bar", "baz"},
		"X-Custom":     []string{"foo", "bar", "baz"},
	}
	backend.ResponseHeaders = responseHeaders

	responses := makeNRequests(handler, 2, buildGetRequest("http://somehost.com/assets/1"))
	assert.Equal(t, responseHeaders, responses[0].Header, "Cache didn't send same headers that backend originally sent")
}

func TestCacheByHeaders(t *testing.T) {
	handler, backend := buildBasicHandler()

	handler.Config.CacheRules = append(handler.Config.CacheRules, &HeaderCacheRule{
		Header: "Content-Type",
		Value:  []string{"image/png", "image/gif"},
	})

	// First requests with png images that should be cached
	backend.ResponseHeaders = http.Header{"Content-Type": []string{"image/png"}}
	makeNRequests(handler, 5, buildGetRequest("http://somehost.com/another_not_cached_path/png"))
	assert.Equal(t, 1, backend.timesCalled, "Cache should have been called once, but it was called", backend.timesCalled)

	// Second requests with gifs that should also be cached
	backend.ResponseHeaders = http.Header{"Content-Type": []string{"image/gif"}}
	makeNRequests(handler, 2, buildGetRequest("http://somehost.com/another_not_cached_path/gif"))
	assert.Equal(t, 2, backend.timesCalled, "Cache should have been called twice but is was called", backend.timesCalled)

	// Third request with videos that should not be cached
	backend.ResponseHeaders = http.Header{"Content-Type": []string{"video/mp4"}}
	makeNRequests(handler, 10, buildGetRequest("http://somehost.com/another_not_cached_path/mp4"))
	assert.Equal(t, 12, backend.timesCalled, "Cache should have been called 12 times but was called", backend.timesCalled)
}

func TestVaryAll(t *testing.T) {
	handler, backend := buildBasicHandler()

	backend.ResponseHeaders = http.Header{
		"Vary":          []string{"*"},
		"Cache-Control": []string{"max-age=3600"},
	}

	makeNRequests(handler, 2, buildGetRequest("http://somehost.com/assets/1"))
	assert.Equal(t, 2, backend.timesCalled, "Invalid number of times called")
}

func TestVaryAcceptEncoding(t *testing.T) {
	handler, backend := buildBasicHandler()

	backend.ResponseHeaders = http.Header{
		"Vary":          []string{"Accept-Encoding"},
		"Cache-Control": []string{"max-age=3600"},
	}

	makeNRequests(handler, 2, buildRequest("http://somehost.com/assets/1", "GET", http.Header{
		"Accept-Encoding": {"gzip"},
	}))
	assert.Equal(t, 1, backend.timesCalled, "Invalid number of times called")

	makeNRequests(handler, 3, buildRequest("http://somehost.com/assets/1", "GET", http.Header{
		"Accept-Encoding": {"deflate"},
	}))
	assert.Equal(t, 2, backend.timesCalled, "Invalid number of times called")

}

func TestVaryWithTwoHeaders(t *testing.T) {
	handler, backend := buildBasicHandler()

	backend.ResponseHeaders = http.Header{
		"Vary":          []string{"Accept-Encoding, User-Agent"},
		"Cache-Control": []string{"max-age=3600"},
	}

	makeNRequests(handler, 2, buildRequest("http://somehost.com/assets/1", "GET", http.Header{
		"Accept-Encoding": {"gzip"},
		"User-Agent":      {"Mobile"},
		"X-Another":       {"x"},
	}))
	assert.Equal(t, 1, backend.timesCalled, "Invalid number of times called")

	makeNRequests(handler, 2, buildRequest("http://somehost.com/assets/1", "GET", http.Header{
		"Accept-Encoding": {"gzip"},
		"User-Agent":      {"Mobile"},
		"X-Another":       {"Y"},
	}))
	assert.Equal(t, 1, backend.timesCalled, "Invalid number of times called")

	makeNRequests(handler, 3, buildRequest("http://somehost.com/assets/1", "GET", http.Header{
		"Accept-Encoding": {"gzip"},
		"User-Agent":      {"Desktop"},
		"X-Another":       {"X"},
	}))
	assert.Equal(t, 2, backend.timesCalled, "Invalid number of times called")
}

func TestStatusCacheSkip(t *testing.T) {
	handler, _ := buildBasicHandler()
	handler.Config.StatusHeader = "cache-status"

	reqUrl, _ := url.Parse("http://somehost.com/assets/some.jpg")
	responses := makeNRequests(handler, 1, &http.Request{Method: "POST", URL: reqUrl})

	assert.Equal(t, []string{"skip"}, responses[0].Header["Cache-Status"])
}

func TestStatusCacheHit(t *testing.T) {
	handler, backend := buildBasicHandler()
	handler.Config.StatusHeader = "Cache-Status"

	backend.ResponseHeaders = http.Header{
		"Cache-control": []string{"public; max-age=3600"},
	}

	responses := makeNRequests(handler, 1, buildGetRequest("http://somehost.com/"))
	assert.Equal(t, []string{"miss"}, responses[0].Header["Cache-Status"])

	responses = makeNRequests(handler, 1, buildGetRequest("http://somehost.com/"))
	assert.Equal(t, []string{"hit"}, responses[0].Header["Cache-Status"])
}

func TestExpiration(t *testing.T) {
	handler, backend := buildBasicHandler()

	backend.ResponseHeaders = http.Header{
		"Cache-control": []string{"public; max-age=1"},
	}

	makeNRequests(handler, 1, buildGetRequest("http://somehost.com/"))
	assert.Equal(t, 1, backend.TimesCalled(), "Backend was not called")

	time.Sleep(time.Duration(1010) * time.Millisecond)

	makeNRequests(handler, 1, buildGetRequest("http://somehost.com/"))
	assert.Equal(t, 2, backend.TimesCalled(), "Backend should have been called twice")
}

// This is a good test to detect race conditions
func TestFastExpiration(t *testing.T) {
	handler, backend := buildBasicHandler()
	handler.Config.DefaultMaxAge = 1
	handler.Config.CacheRules = append(handler.Config.CacheRules, &PathCacheRule{Path: "/assets"})

	makeNRequests(handler, 1, buildGetRequest("http://somehost.com/assets/1"))
	assert.Equal(t, 1, backend.TimesCalled(), "Backend was not called")

	time.Sleep(time.Duration(1) * time.Millisecond)

	makeNRequests(handler, 1, buildGetRequest("http://somehost.com/assets/1"))
	assert.Equal(t, 2, backend.TimesCalled(), "Backend should have been called twice")
}

func TestStatusCode(t *testing.T) {
	handler, backend := buildBasicHandler()

	backend.ResponseCode = 404
	backend.ResponseHeaders = http.Header{
		"Cache-control": []string{"public; max-age=3600"},
	}

	responses := makeNRequests(handler, 1, buildGetRequest("http://somehost.com/"))
	assert.Equal(t, 404, responses[0].StatusCode)

	responses = makeNRequests(handler, 1, buildGetRequest("http://somehost.com/"))
	assert.Equal(t, 404, responses[0].StatusCode)
}

/**
 *
 * Locking tests
 *
 */
func TestLockOnCacheableReq(t *testing.T) {
	handler, backend := buildBasicHandler()

	backend.Latency = time.Duration(10) * time.Millisecond
	backend.ResponseHeaders = http.Header{
		"Cache-control": []string{"public; max-age=3600"},
	}

	go makeNConcurrentRequests(handler, 10, buildGetRequest("http://somehost.com/"))
	time.Sleep(time.Duration(5) * time.Millisecond)
	assert.Equal(t, 1, backend.ConcurrencyLevel(), "There are more conccurrent requests than expected")
	assert.Equal(t, 1, backend.MaxConcurrencyLevel(), "There were more conccurrent requests than expected")
	assert.Equal(t, 1, backend.TimesCalled(), "Backend was called more times than expected")

	time.Sleep(time.Duration(10) * time.Millisecond)
	assert.Equal(t, 0, backend.ConcurrencyLevel(), "There are more conccurrent requests than expected")
	assert.Equal(t, 1, backend.MaxConcurrencyLevel(), "There were more conccurrent requests than expected")
	assert.Equal(t, 1, backend.TimesCalled(), "Backend was called more times than expected")
}

func TestLockOnNonCacheableReq(t *testing.T) {
	handler, backend := buildBasicHandler()

	backend.Latency = time.Duration(10) * time.Millisecond
	backend.TimeToEnd = time.Duration(10) * time.Millisecond
	backend.ResponseHeaders = http.Header{
		"Cache-control": []string{"private"},
	}

	go makeNConcurrentRequests(handler, 10, buildGetRequest("http://somehost.com/"))
	time.Sleep(time.Duration(5) * time.Millisecond)
	assert.Equal(t, 1, backend.ConcurrencyLevel(), "There are more conccurrent requests than expected")
	assert.Equal(t, 1, backend.MaxConcurrencyLevel(), "There were more conccurrent requests than expected")
	assert.Equal(t, 1, backend.TimesCalled(), "Backend was called more times than expected")

	time.Sleep(time.Duration(10) * time.Millisecond)
	assert.Equal(t, 10, backend.ConcurrencyLevel(), "The locking is incorrect")
	assert.Equal(t, 10, backend.MaxConcurrencyLevel(), "The locking is incorrect")
	assert.Equal(t, 10, backend.TimesCalled(), "Backend was called different times than expected")

	time.Sleep(time.Duration(10) * time.Millisecond)
	assert.Equal(t, 9, backend.ConcurrencyLevel(), "The locking is incorrect")
	assert.Equal(t, 10, backend.MaxConcurrencyLevel(), "The locking is incorrect")
	assert.Equal(t, 10, backend.TimesCalled(), "Backend was called different times than expected")

	time.Sleep(time.Duration(10) * time.Millisecond)
	assert.Equal(t, 0, backend.ConcurrencyLevel(), "The locking is incorrect")
	assert.Equal(t, 10, backend.MaxConcurrencyLevel(), "The locking is incorrect")
	assert.Equal(t, 10, backend.TimesCalled(), "Backend was called different times than expected")
}

func TestLockOnVaryHeaderRequests(t *testing.T) {
	handler, backend := buildBasicHandler()

	backend.Latency = time.Duration(10) * time.Millisecond
	backend.TimeToEnd = time.Duration(10) * time.Millisecond
	backend.ResponseHeaders = http.Header{
		"Vary":          []string{"Accept-Encoding"},
		"Cache-control": []string{"public; max-age=3600"},
	}

	go makeNConcurrentRequests(handler, 10, buildRequest("http://somehost.com/", "GET", http.Header{
		"Accept-Encoding": {"gzip"},
	}))
	go makeNConcurrentRequests(handler, 10, buildRequest("http://somehost.com/", "GET", http.Header{
		"Accept-Encoding": {"deflate"},
	}))
	go makeNConcurrentRequests(handler, 10, buildRequest("http://somehost.com/", "GET", http.Header{
		"Accept-Encoding": {"somestrangeEncoding"},
	}))

	time.Sleep(time.Duration(5) * time.Millisecond)
	assert.Equal(t, 1, backend.ConcurrencyLevel(), "There are more conccurrent requests than expected")
	assert.Equal(t, 1, backend.MaxConcurrencyLevel(), "There were more conccurrent requests than expected")
	assert.Equal(t, 1, backend.TimesCalled(), "Backend was called more times than expected")

	time.Sleep(time.Duration(10) * time.Millisecond)
	assert.Equal(t, 2, backend.ConcurrencyLevel(), "The locking is incorrect")
	assert.Equal(t, 2, backend.MaxConcurrencyLevel(), "The locking is incorrect")
	assert.Equal(t, 2, backend.TimesCalled(), "Backend was called different times than expected")

	time.Sleep(time.Duration(10) * time.Millisecond)
	assert.Equal(t, 2, backend.ConcurrencyLevel(), "The locking is incorrect")
	assert.Equal(t, 2, backend.MaxConcurrencyLevel(), "The locking is incorrect")
	assert.Equal(t, 3, backend.TimesCalled(), "Backend was called different times than expected")

	time.Sleep(time.Duration(10) * time.Millisecond)
	assert.Equal(t, 1, backend.ConcurrencyLevel(), "The locking is incorrect")
	assert.Equal(t, 2, backend.MaxConcurrencyLevel(), "The locking is incorrect")
	assert.Equal(t, 3, backend.TimesCalled(), "Backend was called different times than expected")


	time.Sleep(time.Duration(10) * time.Millisecond)
	assert.Equal(t, 0, backend.ConcurrencyLevel(), "The locking is incorrect")
	assert.Equal(t, 2, backend.MaxConcurrencyLevel(), "The locking is incorrect")
	assert.Equal(t, 3, backend.TimesCalled(), "Backend was called different times than expected")
}

func TestLockOnMixedContent(t *testing.T) {
	handler, backend := buildBasicHandler()

	backend.Latency = time.Duration(10) * time.Millisecond
	backend.ResponseHeaders = http.Header{
		"Cache-control": []string{"private"},
	}

	go makeNConcurrentRequests(handler, 3, buildGetRequest("http://somehost.com/"))
	time.Sleep(time.Duration(5) * time.Millisecond)
	assert.Equal(t, 1, backend.ConcurrencyLevel(), "There are more conccurrent requests than expected")
	assert.Equal(t, 1, backend.MaxConcurrencyLevel(), "There were more conccurrent requests than expected")
	assert.Equal(t, 1, backend.TimesCalled(), "Backend was called more times than expected")

	time.Sleep(time.Duration(10) * time.Millisecond)
	assert.Equal(t, 2, backend.ConcurrencyLevel(), "The locking is incorrect")
	assert.Equal(t, 2, backend.MaxConcurrencyLevel(), "The locking is incorrect")
	assert.Equal(t, 3, backend.TimesCalled(), "Backend was called different times than expected")

	time.Sleep(time.Duration(10) * time.Millisecond)
	assert.Equal(t, 0, backend.ConcurrencyLevel(), "The locking is incorrect")

	// After concurrent non cached requests ends
	// The next one is cacheable
	// So only one request should be made

	backend.ResponseHeaders = http.Header{
		"Cache-control": []string{"public; max-age=3600"},
	}
	makeNRequests(handler, 3, buildGetRequest("http://somehost.com/"))
	assert.Equal(t, 4, backend.TimesCalled(), "Backend was called different times than expected")
}

/* Mmap test */
func TestMMapWritesToDisk(t *testing.T) {
	cache := NewCache(NewMMapStorage("/tmp/caddy-cache-tests"))
	cache.Setup()
	handler, backend := buildHandlerWithCache(cache)

	content := []byte("Some content")
	backend.ResponseBody = content
	backend.ResponseHeaders = http.Header{"Cache-control": []string{"public; max-age=1"}}

	req := buildGetRequest("http://somehost.com/")
	makeNRequests(handler, 1, req)

	err := cache.GetOrSet(getKey(req), func(entry *HttpCacheEntry) bool { return true }, func(entry *HttpCacheEntry) (*HttpCacheEntry, error) {
		assert.NotNil(t, entry, "Entry was not found")
		assert.NotNil(t, entry.Response.Body, "Body was not saved")
		fileName := entry.Response.Body.(*MMapContent).file.Name()
		savedContent, err := ioutil.ReadFile(fileName)
		assert.NoError(t, err, "Failed reading disk response")
		assert.Equal(t, content, savedContent, "Content on disk is not the same")
		return nil, nil
	})
	assert.NoError(t, err, "There was an error in GetOrLock")
}


func TestStreamContent(t *testing.T) {
/*	handler, backend := buildBasicHandler()

	backend.Latency = time.Duration(10) * time.Millisecond
	backend.TimeToEnd = time.Duration(10) * time.Millisecond
	backend.ResponseHeaders = http.Header{ "Cache-control": []string{"public; max-age=3600"} }

	go makeNConcurrentRequests(handler, 2, buildGetRequest("http://somehost.com/"))
	time.Sleep(time.Duration(5) * time.Millisecond)
	assert.Equal(t, 1, backend.ConcurrencyLevel(), "There are more conccurrent requests than expected")
	assert.Equal(t, 1, backend.MaxConcurrencyLevel(), "There were more conccurrent requests than expected")
	assert.Equal(t, 1, backend.TimesCalled(), "Backend was called more times than expected")

	time.Sleep(time.Duration(15) * time.Millisecond)
*/
}