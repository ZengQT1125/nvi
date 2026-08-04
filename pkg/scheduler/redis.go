package scheduler

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

var (
	// LuaAcquireKey 执行经典 Nginx 风格的平滑加权轮询（SWRR）：
	//   对每个可用 key：currentWeight[k] += weight[k]，并选出 currentWeight 最大的；
	//   将所选 key 的 currentWeight 减去本轮 totalWeight。
	// 注意：所有访问的 key（active/dead/current_weights/cooling）都通过 KEYS 显式声明，
	// 这样在 Redis Cluster 模式下用 hash tag 可统一映射到同一 slot；
	// 而 concurrency:<key> 是动态名，单机模式下能跑，集群部署需要进一步用 hash tag 化（后续工作）。
	LuaAcquireKey = redis.NewScript(`
local active_keys = KEYS[1]
local dead_keys = KEYS[2]
local current_weights = KEYS[3]
local cooling_keys = KEYS[4]

local max_concurrency = tonumber(ARGV[1])
local lock_ttl = tonumber(ARGV[2])
local now = tonumber(ARGV[3])

local weighted = redis.call("ZRANGE", active_keys, 0, -1, "WITHSCORES")
local selected_key = ""
local selected_weight = nil
local total_weight = 0

for i = 1, #weighted, 2 do
	local key = weighted[i]
	local weight = tonumber(weighted[i + 1]) or 0
	if redis.call("SISMEMBER", dead_keys, key) == 0 then
		local cool_until = redis.call("HGET", cooling_keys, key)
		if (not cool_until) or tonumber(cool_until) < now then
			local c_key = "concurrency:" .. key
			local current = tonumber(redis.call("GET", c_key) or "0")
			if current < max_concurrency then
				local new_weight = tonumber(redis.call("HGET", current_weights, key) or "0") + weight
				redis.call("HSET", current_weights, key, new_weight)
				total_weight = total_weight + weight
				if (not selected_weight) or new_weight > selected_weight then
					selected_key = key
					selected_weight = new_weight
				end
			end
		end
	end
end

if selected_key == "" then
	return ""
end

redis.call("HINCRBYFLOAT", current_weights, selected_key, -total_weight)
local concurrency_key = "concurrency:" .. selected_key
redis.call("INCR", concurrency_key)
redis.call("EXPIRE", concurrency_key, lock_ttl)
return selected_key
		`)

	LuaReleaseKey = redis.NewScript(`
local c_key = "concurrency:" .. KEYS[1]
local current = tonumber(redis.call("GET", c_key) or "0")
if current > 0 then
	redis.call("DECR", c_key)
end
return 1
		`)

	LuaTryAcquireSpecificKey = redis.NewScript(`
local active_keys = KEYS[1]
local dead_keys = KEYS[2]
local cooling_keys = KEYS[3]

local key = ARGV[1]
local max_concurrency = tonumber(ARGV[2])
local lock_ttl = tonumber(ARGV[3])
local now = tonumber(ARGV[4])

if redis.call("ZSCORE", active_keys, key) == false then
	return 0
end
if redis.call("SISMEMBER", dead_keys, key) == 1 then
	return 0
end
local cool_until = redis.call("HGET", cooling_keys, key)
if cool_until and tonumber(cool_until) >= now then
	return 0
end
local c_key = "concurrency:" .. key
local current = tonumber(redis.call("GET", c_key) or "0")
if current >= max_concurrency then
	return 0
end
redis.call("INCR", c_key)
redis.call("EXPIRE", c_key, lock_ttl)
return 1
		`)
)

type Stats struct {
	Active  int `json:"active"`
	Cooling int `json:"cooling"`
	Dead    int `json:"dead"`
}

type localKey struct {
	key           string
	weight        float64
	currentWeight float64
}

type Scheduler struct {
	client      *redis.Client
	mu          sync.Mutex
	active      []localKey
	cooling     map[string]int64
	dead        map[string]struct{}
	concurrency map[string]int
}

func NewScheduler(client *redis.Client) *Scheduler {
	return &Scheduler{
		client:      client,
		cooling:     make(map[string]int64),
		dead:        make(map[string]struct{}),
		concurrency: make(map[string]int),
	}
}

func (s *Scheduler) Client() *redis.Client {
	return s.client
}

func (s *Scheduler) AddKey(ctx context.Context, key string, weight float64) error {
	if s == nil {
		return nil
	}
	if s.client == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		for i := range s.active {
			if s.active[i].key == key {
				s.active[i].weight = weight
				return nil
			}
		}
		s.active = append(s.active, localKey{key: key, weight: weight})
		return nil
	}
	return s.client.ZAdd(ctx, "nvidia:keys:active", redis.Z{Score: weight, Member: key}).Err()
}

func (s *Scheduler) AcquireKey(ctx context.Context, maxConcurrency int) (string, error) {
	if s == nil {
		return "", nil
	}
	if s.client == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		now := time.Now().Unix()
		for key, until := range s.cooling {
			if until < now {
				delete(s.cooling, key)
			}
		}

		selectedIndex := -1
		selectedWeight := 0.0
		totalWeight := 0.0
		for i := range s.active {
			item := &s.active[i]
			if _, dead := s.dead[item.key]; dead {
				continue
			}
			if until, cooling := s.cooling[item.key]; cooling && until >= now {
				continue
			}
			if s.concurrency[item.key] >= maxConcurrency {
				continue
			}
			item.currentWeight += item.weight
			totalWeight += item.weight
			if selectedIndex == -1 || item.currentWeight > selectedWeight {
				selectedIndex = i
				selectedWeight = item.currentWeight
			}
		}
		if selectedIndex == -1 {
			return "", nil
		}
		s.active[selectedIndex].currentWeight -= totalWeight
		s.concurrency[s.active[selectedIndex].key]++
		return s.active[selectedIndex].key, nil
	}
	res, err := LuaAcquireKey.Run(ctx, s.client,
		[]string{"nvidia:keys:active", "nvidia:keys:dead", "key_current_weight", "key_cooling"},
		maxConcurrency, 60, time.Now().Unix()).Result()
	if err == redis.Nil {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	str, ok := res.(string)
	if !ok || str == "" {
		return "", nil
	}
	return str, nil
}

func (s *Scheduler) TryAcquireSpecificKey(ctx context.Context, key string, maxConcurrency int) (bool, error) {
	if s == nil {
		return false, nil
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return false, nil
	}
	if maxConcurrency <= 0 {
		maxConcurrency = 1
	}
	if s.client == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		now := time.Now().Unix()
		for coolingKey, until := range s.cooling {
			if until < now {
				delete(s.cooling, coolingKey)
			}
		}
		active := false
		for i := range s.active {
			if s.active[i].key == key {
				active = true
				break
			}
		}
		if !active {
			return false, nil
		}
		if _, dead := s.dead[key]; dead {
			return false, nil
		}
		if until, cooling := s.cooling[key]; cooling && until >= now {
			return false, nil
		}
		if s.concurrency[key] >= maxConcurrency {
			return false, nil
		}
		s.concurrency[key]++
		return true, nil
	}
	res, err := LuaTryAcquireSpecificKey.Run(ctx, s.client,
		[]string{"nvidia:keys:active", "nvidia:keys:dead", "key_cooling"},
		key, maxConcurrency, 60, time.Now().Unix()).Result()
	if err == redis.Nil {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	switch v := res.(type) {
	case int64:
		return v > 0, nil
	case string:
		return strings.TrimSpace(v) == "1", nil
	default:
		return false, nil
	}
}

func (s *Scheduler) ReleaseKey(ctx context.Context, key string) error {
	if s == nil || key == "" {
		return nil
	}
	if s.client == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.concurrency[key] > 0 {
			s.concurrency[key]--
		}
		return nil
	}
	return LuaReleaseKey.Run(ctx, s.client, []string{key}).Err()
}

func (s *Scheduler) MarkCooling(ctx context.Context, key string, duration time.Duration) error {
	if s == nil {
		return nil
	}
	coolUntil := time.Now().Add(duration).Unix()
	if s.client == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.cooling[key] = coolUntil
		return nil
	}
	return s.client.HSet(ctx, "key_cooling", key, coolUntil).Err()
}

func (s *Scheduler) MarkDead(ctx context.Context, key string) error {
	if s == nil {
		return nil
	}
	if s.client == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.dead[key] = struct{}{}
		// 把已死 key 的累计加权重置为 0，避免它在 Restore 之后立刻"借旧账"抢调度。
		for i := range s.active {
			if s.active[i].key == key {
				s.active[i].currentWeight = 0
				break
			}
		}
		// 同步清掉并发计数，否则一旦标记 Dead，对应槽位会被永远占用，
		// 影响后续 RestoreRecoverableStatuses 把它重新拉回 Active 时的容量判定。
		delete(s.concurrency, key)
		return nil
	}
	if err := s.client.SAdd(ctx, "nvidia:keys:dead", key).Err(); err != nil {
		return err
	}
	// 在 Redis 端也把这条 key 的累计加权移除，防止 key_current_weight 哈希长期膨胀。
	// HDel 失败不致命：下次 Lua 调用读到旧值最多影响一次调度选择。
	_ = s.client.HDel(ctx, "key_current_weight", key).Err()
	return nil
}

func (s *Scheduler) Reset(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if s.client == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.active = nil
		s.cooling = make(map[string]int64)
		s.dead = make(map[string]struct{})
		s.concurrency = make(map[string]int)
		return nil
	}
	keys, err := s.client.ZRange(ctx, "nvidia:keys:active", 0, -1).Result()
	if err != nil {
		if isRedisUnavailable(err) {
			return nil
		}
		return err
	}
	redisKeys := make([]string, 0, len(keys)+4)
	redisKeys = append(redisKeys, "nvidia:keys:active", "nvidia:keys:dead", "key_cooling", "key_current_weight")
	for _, key := range keys {
		redisKeys = append(redisKeys, "concurrency:"+key)
	}
	if len(redisKeys) > 0 {
		if err := s.client.Del(ctx, redisKeys...).Err(); err != nil {
			if isRedisUnavailable(err) {
				return nil
			}
			return err
		}
	}
	return nil
}

// Stats 是只读的 getter，**不再**做过期 cooling 条目的清理；
// 过期清理由 AcquireKey / TryAcquireSpecificKey 自然完成，或由调用方显式 CleanupExpired。
// 这样 Stats 在 dashboard 高频刷新下不会反复 HDEL，副作用面更小。
func (s *Scheduler) Stats(ctx context.Context) (*Stats, error) {
	if s == nil {
		return &Stats{}, nil
	}
	if s.client == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		now := time.Now().Unix()
		cooling := 0
		for _, until := range s.cooling {
			if until >= now {
				cooling++
			}
		}
		return &Stats{Active: len(s.active), Cooling: cooling, Dead: len(s.dead)}, nil
	}
	active, err := s.client.ZCard(ctx, "nvidia:keys:active").Result()
	if err != nil {
		if isRedisUnavailable(err) {
			return &Stats{}, nil
		}
		return nil, err
	}
	dead, err := s.client.SCard(ctx, "nvidia:keys:dead").Result()
	if err != nil {
		if isRedisUnavailable(err) {
			return &Stats{}, nil
		}
		return nil, err
	}
	coolingEntries, err := s.client.HGetAll(ctx, "key_cooling").Result()
	if err != nil {
		if isRedisUnavailable(err) {
			return &Stats{}, nil
		}
		return nil, err
	}
	now := time.Now().Unix()
	cooling := 0
	for _, until := range coolingEntries {
		unixTs, convErr := strconv.ParseInt(until, 10, 64)
		if convErr != nil {
			continue
		}
		if unixTs >= now {
			cooling++
		}
	}
	return &Stats{Active: int(active), Cooling: cooling, Dead: int(dead)}, nil
}

// CleanupExpired 显式清理过期的 cooling 条目；后台 ticker 可以周期调用，
// 避免 Stats 这个只读接口承担删除职责。
func (s *Scheduler) CleanupExpired(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if s.client == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		now := time.Now().Unix()
		for key, until := range s.cooling {
			if until < now {
				delete(s.cooling, key)
			}
		}
		return nil
	}
	coolingEntries, err := s.client.HGetAll(ctx, "key_cooling").Result()
	if err != nil {
		if isRedisUnavailable(err) {
			return nil
		}
		return err
	}
	now := time.Now().Unix()
	expired := make([]string, 0, len(coolingEntries))
	for key, until := range coolingEntries {
		unixTs, convErr := strconv.ParseInt(until, 10, 64)
		if convErr != nil {
			continue
		}
		if unixTs < now {
			expired = append(expired, key)
		}
	}
	if len(expired) == 0 {
		return nil
	}
	return s.client.HDel(ctx, "key_cooling", expired...).Err()
}

func isRedisUnavailable(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "connectex") ||
		strings.Contains(message, "connection refused") ||
		strings.Contains(message, "failed to dial") ||
		strings.Contains(message, "no such host") ||
		strings.Contains(message, "i/o timeout")
}
