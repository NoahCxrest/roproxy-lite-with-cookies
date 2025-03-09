package main

import (
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/valyala/fasthttp"
)

var timeout, _ = strconv.Atoi(os.Getenv("TIMEOUT"))
var retries, _ = strconv.Atoi(os.Getenv("RETRIES"))
var port = os.Getenv("PORT")

var client *fasthttp.Client

type CacheEntry struct {
	ExpiresAt  time.Time
	StatusCode int
	Headers    map[string]string
	Body       []byte
}

var cache sync.Map
var cacheDuration = 5 * time.Minute

func main() {
	h := requestHandler

	client = &fasthttp.Client{
		ReadTimeout:         time.Duration(timeout) * time.Second,
		MaxIdleConnDuration: 60 * time.Second,
	}

	if err := fasthttp.ListenAndServe(":"+port, h); err != nil {
		log.Fatalf("Error in ListenAndServe: %s", err)
	}
}

func requestHandler(ctx *fasthttp.RequestCtx) {
	val, ok := os.LookupEnv("KEY")

	if ok && string(ctx.Request.Header.Peek("PROXYKEY")) != val {
		ctx.SetStatusCode(407)
		ctx.SetBody([]byte("Missing or invalid PROXYKEY header."))
		return
	}

	if len(strings.SplitN(string(ctx.Request.Header.RequestURI())[1:], "/", 2)) < 2 {
		ctx.SetStatusCode(400)
		ctx.SetBody([]byte("URL format invalid."))
		return
	}

	url := strings.SplitN(string(ctx.Request.Header.RequestURI())[1:], "/", 2)
	fullURL := "https://" + url[0] + ".roblox.com/" + url[1]
	cacheKey := fullURL + "|" + string(ctx.Request.Header.Peek("Cookie"))

	if entry, found := cache.Load(cacheKey); found {
		cacheEntry := entry.(CacheEntry)
		if time.Now().Before(cacheEntry.ExpiresAt) {
			ctx.SetStatusCode(cacheEntry.StatusCode)
			ctx.SetBody(cacheEntry.Body)
			for k, v := range cacheEntry.Headers {
				ctx.Response.Header.Set(k, v)
			}
			return
		}
		cache.Delete(cacheKey)
	}

	response := makeRequest(ctx, 1, cacheKey)
	defer fasthttp.ReleaseResponse(response)

	body := response.Body()
	ctx.SetBody(body)
	ctx.SetStatusCode(response.StatusCode())
	response.Header.VisitAll(func(key, value []byte) {
		ctx.Response.Header.Set(string(key), string(value))
	})
}

func makeRequest(ctx *fasthttp.RequestCtx, attempt int, cacheKey string) *fasthttp.Response {
	if attempt > retries {
		resp := fasthttp.AcquireResponse()
		resp.SetBody([]byte("Proxy failed to connect. Please try again."))
		resp.SetStatusCode(500)
		return resp
	}

	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	req.Header.SetMethod(string(ctx.Method()))
	url := strings.SplitN(string(ctx.Request.Header.RequestURI())[1:], "/", 2)
	req.SetRequestURI("https://" + url[0] + ".roblox.com/" + url[1])
	req.SetBody(ctx.Request.Body())

	// Copy all headers from original request
	ctx.Request.Header.VisitAll(func(key, value []byte) {
		if !strings.EqualFold(string(key), "PROXYKEY") {
			req.Header.Set(string(key), string(value))
		}
	})

	resp := fasthttp.AcquireResponse()
	err := client.Do(req, resp)

	if err != nil {
		fasthttp.ReleaseResponse(resp)
		return makeRequest(ctx, attempt+1, cacheKey)
	}

	if resp.StatusCode() == 200 {
		headers := make(map[string]string)
		resp.Header.VisitAll(func(key, value []byte) {
			headers[string(key)] = string(value)
		})

		cache.Store(cacheKey, CacheEntry{
			ExpiresAt:  time.Now().Add(cacheDuration),
			StatusCode: resp.StatusCode(),
			Headers:    headers,
			Body:       append([]byte(nil), resp.Body()...),
		})
	}

	return resp
}
