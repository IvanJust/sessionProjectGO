package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

func formatMillis(t time.Time) string { return strconv.FormatInt(t.UnixMilli(), 10) }

type Limiter struct{ client *redis.Client }

func NewLimiter(client *redis.Client) *Limiter { return &Limiter{client: client} }

var incrementLimit = redis.NewScript(`
local count = redis.call('INCR', KEYS[1])
if count == 1 then redis.call('PEXPIRE', KEYS[1], ARGV[1]) end
if count > tonumber(ARGV[2]) then return redis.call('PTTL', KEYS[1]) end
return 0
`)

// Allow uses a fixed window. The returned duration is positive when blocked.
func (l *Limiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (time.Duration, error) {
	digest := sha256.Sum256([]byte(key))
	ms, err := incrementLimit.Run(ctx, l.client, []string{"auth:limit:" + hex.EncodeToString(digest[:])}, window.Milliseconds(), limit).Int64()
	return time.Duration(ms) * time.Millisecond, err
}
