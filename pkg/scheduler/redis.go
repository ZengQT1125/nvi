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
local model_cooling_keys = KEYS[5]

local max_concurrency = tonumber(ARGV[1])
local lock_ttl = tonumber(ARGV[2])
local now = tonumber(ARGV[3])
local model = ARGV[4]

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
			local model_cool_until = nil
			if model ~= "" then
				model_cool_until = redis.call("HGET", model_cooling_keys, key .. "|" .. model)
			end
			if (not model_cool_until) or tonumber(model_cool_until) < now then
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
local model_cooling_keys = KEYS[4]

local key = ARGV[1]
local max_concurrency = tonumber(ARGV[2])
local lock_ttl = tonumber(ARGV[3])
local now = tonumber(ARGV[4])
local model = ARGV[5]

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
if model ~= "" then
	local model_cool_until = redis.call("HGET", model_cooling_keys, key .. "|" .. model)
	if model_cool_until and tonumber(model_cool_until) >= now then
		return 0
	end
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

const (
	// key_base_weight / key_health_mult 把「用户配置的基础权重」与「健康评分推导的乘数」
	// 分开存储：实际参与调度的权重 = 基础权重 × 健康乘数，写入 nvidia:keys:active 的 ZSET score。
	//
	// 为什么必须分开：
	//   - 基础权重由 LoadActiveKeys 每 5 分钟（以及管理端增删改 key 时）重写；
	//     健康乘数由 30 秒一次的健康评估循环刷新。
	//   - 若两者都塞进 ZSET score，5 分钟一次的刷新会把健康乘数抹掉；
	//   - 若直接在 ZSET score 上反复相乘，权重会随刷新次数复利放大/缩小。
	keyBaseWeightHashKey = "key_base_weight"
	keyHealthMultHashKey = "key_health_mult"

	// 健康乘数上下限（口径与 AQUA Platform 的「健康分 → 权重乘数」一致）：
	// 下限避免低分 key 被彻底饿死（硬闸门由冷却机制负责），
	// 上限避免单个高分 key 吸走全部流量。
	healthMultiplierMin = 0.5
	healthMultiplierMax = 2.0
)

type Stats struct {
	Active  int `json:"active"`
	Cooling int `json:"cooling"`
	Dead    int `json:"dead"`
}

type localKey struct {
	key           string
	baseWeight    float64 // 用户配置的基础权重（由 AddKey 写入）
	healthMult    float64 // 健康评分推导的乘数（由 SetHealthMultiplier 写入）
	weight        float64 // baseWeight × healthMult，SWRR 实际使用的权重
	currentWeight float64
}

type Scheduler struct {
	client      *redis.Client
	mu          sync.Mutex
	active      []localKey
	cooling     map[string]int64
	// modelCooling 记录 (key, model) 维度的冷却，键为 key .. "|" .. model。
	// 同模型 429 后冷却比 key 级更久，避免 60s 恢复后同一模型立刻又撞限流。
	modelCooling map[string]int64
	dead         map[string]struct{}
	concurrency  map[string]int
}

func NewScheduler(client *redis.Client) *Scheduler {
	return &Scheduler{
		client:       client,
		cooling:      make(map[string]int64),
		modelCooling: make(map[string]int64),
		dead:         make(map[string]struct{}),
		concurrency:  make(map[string]int),
	}
}

func (s *Scheduler) Client() *redis.Client {
	return s.client
}

// AddKey 注册（或更新）一个上游 key 的**基础权重**。
// 实际写入 nvidia:keys:active 的 score 是「基础权重 × 该 key 当前的健康乘数」，
// 这样 30 秒一次的健康评估调整乘数时，不会破坏用户在管理后台配置的基础权重。
func (s *Scheduler) AddKey(ctx context.Context, key string, weight float64) error {
	if s == nil {
		return nil
	}
	if s.client == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		for i := range s.active {
			if s.active[i].key == key {
				s.active[i].baseWeight = weight
				s.active[i].weight = weight * s.active[i].healthMult
				return nil
			}
		}
		s.active = append(s.active, localKey{
			key:        key,
			baseWeight: weight,
			healthMult: 1.0,
			weight:     weight,
		})
		return nil
	}
	multiplier, err := s.healthMultiplier(ctx, key)
	if err != nil {
		return err
	}
	// 先落基础权重，再写实际权重：SetHealthMultiplier 依赖前者才能重算 score。
	if err := s.client.HSet(ctx, keyBaseWeightHashKey, key, formatWeight(weight)).Err(); err != nil {
		return err
	}
	return s.client.ZAdd(ctx, "nvidia:keys:active", redis.Z{Score: weight * multiplier, Member: key}).Err()
}

// formatWeight 统一权重的存储格式，保证 HGet → ParseFloat 能无损往返。
func formatWeight(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

// SetHealthMultiplier 由健康评分后台循环（每 30 秒）调用：写入某个 key 的健康乘数，
// 并把「基础权重 × 乘数」重新写回 nvidia:keys:active 的 ZSET score。
//
// 该方法是幂等的：乘数只决定相对权重，重复写入同一个乘数不会产生累积/复利效应。
// 对于不在活跃池中的 key（已删除、已移出）直接忽略，避免把孤立权重写回调度池。
func (s *Scheduler) SetHealthMultiplier(ctx context.Context, key string, multiplier float64) error {
	if s == nil {
		return nil
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}
	multiplier = clampHealthMultiplier(multiplier)

	if s.client == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		for i := range s.active {
			if s.active[i].key == key {
				s.active[i].healthMult = multiplier
				s.active[i].weight = s.active[i].baseWeight * multiplier
				return nil
			}
		}
		// key 不在活跃池中：忽略（与 Redis 分支保持一致）
		return nil
	}

	// 先确认该 key 仍在活跃池中，避免把已删除/已标记 Dead 的 key 重新写回 ZSET。
	if _, err := s.client.ZScore(ctx, "nvidia:keys:active", key).Result(); err != nil {
		if err == redis.Nil {
			return nil
		}
		return err
	}

	baseWeight, err := s.client.HGet(ctx, keyBaseWeightHashKey, key).Float64()
	if err == redis.Nil {
		// 基础权重缺失说明该 key 尚未（或在 Reset 之后还未）经 AddKey 写入，不写入孤立权重。
		return nil
	}
	if err != nil {
		return err
	}

	if err := s.client.HSet(ctx, keyHealthMultHashKey, key, formatWeight(multiplier)).Err(); err != nil {
		return err
	}
	return s.client.ZAdd(ctx, "nvidia:keys:active", redis.Z{Score: baseWeight * multiplier, Member: key}).Err()
}

// healthMultiplier 读取某个 key 已经保存的健康乘数；未保存或值非法时返回 1.0（中性）。
func (s *Scheduler) healthMultiplier(ctx context.Context, key string) (float64, error) {
	raw, err := s.client.HGet(ctx, keyHealthMultHashKey, key).Result()
	if err == redis.Nil {
		return 1.0, nil
	}
	if err != nil {
		return 1.0, err
	}
	value, parseErr := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if parseErr != nil || value <= 0 {
		return 1.0, nil
	}
	return clampHealthMultiplier(value), nil
}

func clampHealthMultiplier(value float64) float64 {
	if value < healthMultiplierMin {
		return healthMultiplierMin
	}
	if value > healthMultiplierMax {
		return healthMultiplierMax
	}
	return value
}

func (s *Scheduler) AcquireKey(ctx context.Context, maxConcurrency int, model string) (string, error) {
	model = strings.TrimSpace(model)
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
		for field, until := range s.modelCooling {
			if until < now {
				delete(s.modelCooling, field)
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
			if model != "" {
				if until, cooling := s.modelCooling[item.key+"|"+model]; cooling && until >= now {
					continue
				}
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
		[]string{"nvidia:keys:active", "nvidia:keys:dead", "key_current_weight", "key_cooling", "model_cooling"},
		maxConcurrency, 60, time.Now().Unix(), model).Result()
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

func (s *Scheduler) TryAcquireSpecificKey(ctx context.Context, key string, maxConcurrency int, model string) (bool, error) {
	if s == nil {
		return false, nil
	}
	key = strings.TrimSpace(key)
	model = strings.TrimSpace(model)
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
		for field, until := range s.modelCooling {
			if until < now {
				delete(s.modelCooling, field)
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
		if model != "" {
			if until, cooling := s.modelCooling[key+"|"+model]; cooling && until >= now {
				return false, nil
			}
		}
		if s.concurrency[key] >= maxConcurrency {
			return false, nil
		}
		s.concurrency[key]++
		return true, nil
	}
	res, err := LuaTryAcquireSpecificKey.Run(ctx, s.client,
		[]string{"nvidia:keys:active", "nvidia:keys:dead", "key_cooling", "model_cooling"},
		key, maxConcurrency, 60, time.Now().Unix(), model).Result()
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

// MarkModelCooling 记录 (key, model) 维度的冷却。
// 模型级冷却独立于 key 级冷却：触发限流的那个模型会冷却更久，
// 而同一个 key 的其他模型只受 key 级冷却约束，可以更早恢复使用。
func (s *Scheduler) MarkModelCooling(ctx context.Context, key, model string, duration time.Duration) error {
	if s == nil || strings.TrimSpace(key) == "" || strings.TrimSpace(model) == "" {
		return nil
	}
	field := key + "|" + model
	coolUntil := time.Now().Add(duration).Unix()
	if s.client == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.modelCooling[field] = coolUntil
		return nil
	}
	return s.client.HSet(ctx, "model_cooling", field, coolUntil).Err()
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
		s.dead = make(map[string]struct{})
		s.concurrency = make(map[string]int)
		// 保留 cooling / modelCooling：冷却状态跨 Reset 存活。
		// 否则 Prober 恢复 key 后 Reset 会把冷却清掉，同一模型立刻又撞限流。
		return nil
	}
	keys, err := s.client.ZRange(ctx, "nvidia:keys:active", 0, -1).Result()
	if err != nil {
		if isRedisUnavailable(err) {
			return nil
		}
		return err
	}
	redisKeys := make([]string, 0, len(keys)+5)
	// 注意：key_cooling / model_cooling 故意不在删除列表里，冷却状态跨 Reset 存活。
	// key_base_weight / key_health_mult 则一并清除：基础权重会在紧随其后的 AddKey 里重建，
	// 健康乘数由 30 秒一次的后台评估循环在下一轮重新写入（最多 30 秒无乘数倾斜，可接受），
	// 这样也避免已删除 key 在两个 Hash 里留下永久残留。
	redisKeys = append(redisKeys, "nvidia:keys:active", "nvidia:keys:dead", "key_current_weight", keyBaseWeightHashKey, keyHealthMultHashKey)
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
		for field, until := range s.modelCooling {
			if until < now {
				delete(s.modelCooling, field)
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
	if len(expired) > 0 {
		if err := s.client.HDel(ctx, "key_cooling", expired...).Err(); err != nil {
			if isRedisUnavailable(err) {
				return nil
			}
			return err
		}
	}
	// 同样清理过期的模型级冷却表。
	modelCoolingEntries, err := s.client.HGetAll(ctx, "model_cooling").Result()
	if err != nil {
		if isRedisUnavailable(err) {
			return nil
		}
		return err
	}
	expiredModel := make([]string, 0, len(modelCoolingEntries))
	for field, until := range modelCoolingEntries {
		unixTs, convErr := strconv.ParseInt(until, 10, 64)
		if convErr != nil {
			continue
		}
		if unixTs < now {
			expiredModel = append(expiredModel, field)
		}
	}
	if len(expiredModel) == 0 {
		return nil
	}
	if err := s.client.HDel(ctx, "model_cooling", expiredModel...).Err(); err != nil {
		if isRedisUnavailable(err) {
			return nil
		}
		return err
	}
	return nil
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
