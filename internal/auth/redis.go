package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// redisKeyPrefix namespaces session keys so the dashboard can share a Redis
// instance with other workloads without colliding.
const redisKeyPrefix = "orrery:session:"

// RedisStore keeps sessions outside the process, which is what allows more
// than one replica: any pod can serve any request, and a restart or a rollout
// does not sign everybody out.
type RedisStore struct {
	client *redis.Client
	idle   time.Duration
}

// NewRedisStore connects to Redis and verifies the connection before returning,
// so a misconfigured URL fails at startup rather than on the first login.
func NewRedisStore(ctx context.Context, rawURL string, idle time.Duration) (*RedisStore, error) {
	opts, err := redis.ParseURL(rawURL)
	if err != nil {
		return nil, fmt.Errorf("session.redisURL: %w", err)
	}
	client := redis.NewClient(opts)

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("connect to redis: %w", err)
	}
	return &RedisStore{client: client, idle: idle}, nil
}

func (r *RedisStore) key(id string) string { return redisKeyPrefix + id }

func (r *RedisStore) Get(ctx context.Context, id string) (*Session, error) {
	raw, err := r.client.Get(ctx, r.key(id)).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, ErrNoSession
		}
		return nil, fmt.Errorf("read session: %w", err)
	}

	var s Session
	if err := json.Unmarshal(raw, &s); err != nil {
		// A value we cannot decode is worse than no value: drop it so the user
		// gets a clean re-login instead of a permanent error.
		_ = r.Delete(ctx, id)
		return nil, ErrNoSession
	}

	// Redis expires on the absolute TTL; the idle timeout is ours to enforce.
	if s.Expired(time.Now(), r.idle) {
		_ = r.Delete(ctx, id)
		return nil, ErrNoSession
	}
	return &s, nil
}

func (r *RedisStore) Put(ctx context.Context, s *Session) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("encode session: %w", err)
	}

	// Let Redis garbage collect at the absolute deadline so expired sessions
	// cannot accumulate if the dashboard is restarted or scaled to zero.
	ttl := time.Until(s.ExpiresAt)
	if s.ExpiresAt.IsZero() {
		ttl = 24 * time.Hour
	}
	if ttl <= 0 {
		return r.Delete(ctx, s.ID)
	}
	if err := r.client.Set(ctx, r.key(s.ID), raw, ttl).Err(); err != nil {
		return fmt.Errorf("write session: %w", err)
	}
	return nil
}

func (r *RedisStore) Delete(ctx context.Context, id string) error {
	if err := r.client.Del(ctx, r.key(id)).Err(); err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

func (r *RedisStore) Close() error { return r.client.Close() }
