package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
)

type Env struct {
	redisDb *redis.Client
}

type RateCheckBody struct {
	IPAddress string `json:"address"`
}

type RateResponse int

const (
	Denied RateResponse = iota
	Allowed
)

var rateLimitScript = redis.NewScript(`
  local current_key = KEYS[1]
  local previous_key = KEYS[2]
  local max_requests = tonumber(ARGV[1])
  local window_seconds = tonumber(ARGV[2])
  local elapsed = tonumber(ARGV[3])

  local prev_count = tonumber(redis.call('GET', previous_key) or '0') or 0
  local current_count = tonumber(redis.call('GET', current_key) or '0') or 0

  local estimated = prev_count * (1 - elapsed) + current_count

  if estimated >= max_requests then
    return 0
  end

  local new_count = redis.call('INCR', current_key)

  if new_count == 1 then
    redis.call('EXPIRE', current_key, window_seconds * 2)
  end

  return 1
  `)

func rateCheck(env *Env) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body RateCheckBody

		err := json.NewDecoder(r.Body).Decode(&body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		maxRequests := 10
		windowSeconds := int64(10)

		ctx := context.Background()
		now := time.Now().UnixMilli()
		windowMillis := windowSeconds * 1000

		currentKeyTime := now / windowMillis
		previousKeyTime := currentKeyTime - 1

		nowInWindow := now % windowMillis
		elapsed := float64(nowInWindow) / float64(windowMillis)

		currentKey := fmt.Sprintf("%s:%d", body.IPAddress, currentKeyTime)
		previousKey := fmt.Sprintf("%s:%d", body.IPAddress, previousKeyTime)

		result, err := rateLimitScript.Run(ctx, env.redisDb, []string{currentKey, previousKey}, maxRequests, windowSeconds, elapsed).Result()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if RateResponse(result.(int64)) == Allowed {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusTooManyRequests)
		}
	})
}

func main() {
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}
	redisDb := redis.NewClient(&redis.Options{
		Addr:     redisAddr,
		Password: "",
		DB:       0,
	})
	env := &Env{redisDb: redisDb}
	defer redisDb.Close()

	http.Handle("POST /check", MiddlewareChain(rateCheck(env), Log))
	http.ListenAndServe(":8080", nil)
}
