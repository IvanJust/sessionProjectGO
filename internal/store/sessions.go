package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"

	"sessionProject/internal/auth"
)

const sessionPrefix = "auth:session:"
const maxSessions = 10

type Sessions struct{ client *redis.Client }
type storedSession struct {
	auth.Session
	Owner string `json:"user_id"`
}

func NewSessions(client *redis.Client) *Sessions { return &Sessions{client: client} }
func userKey(id string) string                   { return "auth:user:" + id + ":sessions" }

// Scripts and the owner index prevent partial writes and cross-user revocation.
var createSession = redis.NewScript(`
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', ARGV[4])
local oldest = redis.call('ZRANGE', KEYS[2], 0, -1)
while #oldest >= tonumber(ARGV[6]) do
  redis.call('DEL', ARGV[5] .. oldest[1])
  redis.call('ZREM', KEYS[2], oldest[1])
  table.remove(oldest, 1)
end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
redis.call('ZADD', KEYS[2], ARGV[3], ARGV[7])
local latest = redis.call('ZRANGE', KEYS[2], -1, -1, 'WITHSCORES')
redis.call('PEXPIREAT', KEYS[2], latest[2])
return 1
`)

func (s *Sessions) Create(ctx context.Context, session auth.Session, ttl time.Duration) error {
	data, err := json.Marshal(storedSession{Session: session, Owner: session.UserID})
	if err != nil {
		return err
	}
	return createSession.Run(ctx, s.client, []string{sessionPrefix + session.ID, userKey(session.UserID)}, string(data), ttl.Milliseconds(), session.ExpiresAt.UnixMilli(), time.Now().UnixMilli(), sessionPrefix, maxSessions, session.ID).Err()
}

func decodeSession(data string) (auth.Session, error) {
	var stored storedSession
	if err := json.Unmarshal([]byte(data), &stored); err != nil {
		return auth.Session{}, err
	}
	stored.Session.UserID = stored.Owner
	return stored.Session, nil
}

func (s *Sessions) Get(ctx context.Context, id string) (auth.Session, error) {
	data, err := s.client.Get(ctx, sessionPrefix+id).Result()
	if errors.Is(err, redis.Nil) {
		return auth.Session{}, auth.ErrNotFound
	}
	if err != nil {
		return auth.Session{}, err
	}
	return decodeSession(data)
}

func (s *Sessions) List(ctx context.Context, userID string) ([]auth.Session, error) {
	now := time.Now()
	// Expired index entries are also pruned on creation. Filtering here is enough
	// and avoids racing a concurrent creation when cleaning an index.
	ids, err := s.client.ZRevRangeByScore(ctx, userKey(userID), &redis.ZRangeBy{Min: "(" + formatMillis(now), Max: "+inf"}).Result()
	if err != nil {
		return nil, err
	}
	sessions := make([]auth.Session, 0, len(ids))
	if len(ids) == 0 {
		return sessions, nil
	}
	pipe := s.client.Pipeline()
	commands := make([]*redis.StringCmd, len(ids))
	for i, id := range ids {
		commands[i] = pipe.Get(ctx, sessionPrefix+id)
	}
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, err
	}
	for _, cmd := range commands {
		data, err := cmd.Result()
		if errors.Is(err, redis.Nil) {
			continue
		}
		if err != nil {
			return nil, err
		}
		session, err := decodeSession(data)
		if err != nil {
			return nil, err
		}
		if session.UserID == userID && now.Before(session.ExpiresAt) {
			sessions = append(sessions, session)
		}
	}
	return sessions, nil
}

var revokeSession = redis.NewScript(`
local value = redis.call('GET', KEYS[1])
if not value then return 0 end
if cjson.decode(value).user_id ~= ARGV[1] then return 0 end
redis.call('DEL', KEYS[1])
redis.call('ZREM', KEYS[2], ARGV[2])
return 1
`)

func (s *Sessions) Revoke(ctx context.Context, userID, id string) error {
	n, err := revokeSession.Run(ctx, s.client, []string{sessionPrefix + id, userKey(userID)}, userID, id).Int()
	if err != nil {
		return err
	}
	if n == 0 {
		return auth.ErrNotFound
	}
	return nil
}

var revokeAll = redis.NewScript(`
local ids = redis.call('ZRANGE', KEYS[1], 0, -1)
for _, id in ipairs(ids) do redis.call('DEL', ARGV[1] .. id) end
redis.call('DEL', KEYS[1])
return #ids
`)

func (s *Sessions) RevokeAll(ctx context.Context, userID string) error {
	return revokeAll.Run(ctx, s.client, []string{userKey(userID)}, sessionPrefix).Err()
}
