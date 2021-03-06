/*
Check timertask every seconds
*/

package timertask

import (
	"fmt"
	"github.com/go-redis/redis"
	"gosconf"
	"goslib/api"
	"goslib/gen_server"
	"goslib/logger"
	"goslib/player_rpc"
	"goslib/pool"
	"goslib/redisdb"
	"goslib/utils"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type TimerTask struct {
	pool       *pool.Pool
	taskTicker *time.Ticker
	retry      map[string]int
}

var KEY string
var SERVER string

func Start(hostname string) error {
	KEY = "TIMERTASK_KEYS:" + hostname
	SERVER = "TIMERTASK_SERVER:" + hostname

	_, err := gen_server.Start(SERVER, new(TimerTask))
	return err
}

func Stop() error {
	return gen_server.Stop(SERVER, "shutdown")
}

func Add(key string, runAt int64, playerId string, encode_method string, params interface{}) error {
	writer, err := api.Encode(encode_method, params)
	if err != nil {
		return err
	}
	data, err := writer.GetSendData(0)
	if err != nil {
		return err
	}
	content := fmt.Sprintf("%s:%s", playerId, string(data))
	gen_server.Cast(SERVER, &AddParams{key, runAt, content})
	return nil
}

func Update(key string, runAt int64) {
	gen_server.Cast(SERVER, &UpdateParams{key, runAt})
}

func Finish(key string) {
	gen_server.Cast(SERVER, &FinishParams{key})
}

func Del(key string) {
	gen_server.Cast(SERVER, &DelParams{key})
}

var tickerTaskParams = &TickerTaskParams{}

func (t *TimerTask) Init(args []interface{}) (err error) {
	t.pool, err = pool.New(runtime.NumCPU(), func(args interface{}) (interface{}, error) {
		return nil, t.handleTask(args.(string))
	})
	if err != nil {
		return
	}

	t.taskTicker = time.NewTicker(gosconf.TIMERTASK_CHECK_DURATION)
	t.retry = make(map[string]int)
	go func() {
		for range t.taskTicker.C {
			_, err = gen_server.Call(SERVER, tickerTaskParams)
			if err != nil {
				logger.ERR("timertask tickerTask failed: ", err)
			}
		}
	}()
	return
}

func (t *TimerTask) HandleCall(req *gen_server.Request) (interface{}, error) {
	err := t.handleCallAndCast(req.Msg)
	return nil, err
}

func (t *TimerTask) HandleCast(req *gen_server.Request) {
	_ = t.handleCallAndCast(req.Msg)
}

type FinishParams struct{ key string }
type DelParams struct{ key string }

func (t *TimerTask) handleCallAndCast(msg interface{}) error {
	switch params := msg.(type) {
	case *AddParams:
		return t.handleAdd(params)
	case *UpdateParams:
		return t.handleUpdate(params)
	case *FinishParams:
		t.pool.ProcessAsync(params.key)
		return nil
	case *DelParams:
		return t.del(params.key)
	case *TickerTaskParams:
		t.tickerTask()
	}
	return nil
}

func (t *TimerTask) Terminate(reason string) (err error) {
	t.taskTicker.Stop()
	return nil
}

type AddParams struct {
	key     string
	runAt   int64
	content string
}

func (t *TimerTask) handleAdd(params *AddParams) error {
	return t.add(params.key, params.runAt, params.content)
}

type UpdateParams struct {
	key   string
	runAt int64
}

func (t *TimerTask) handleUpdate(params *UpdateParams) error {
	return t.update(params.key, params.runAt)
}

func mfa_key(key string) string {
	return "timertask:" + key
}

var MFA_EXPIRE_DELAY int64 = 3600

func (t *TimerTask) add(key string, runAt int64, content string) error {
	mfa_expire := utils.MaxInt64(runAt-time.Now().Unix(), 0) + MFA_EXPIRE_DELAY
	if _, err := redisdb.Instance().Set(mfa_key(key), content, time.Duration(mfa_expire)*time.Second).Result(); err != nil {
		return err
	}
	member := redis.Z{
		Score:  float64(runAt),
		Member: key,
	}
	if _, err := redisdb.Instance().ZAdd(KEY, member).Result(); err != nil {
		return err
	}
	return nil
}

func (t *TimerTask) update(key string, runAt int64) error {
	score, err := redisdb.Instance().ZScore(KEY, key).Result()
	if err == redis.Nil {
		return nil
	}
	if err != nil {
		return err
	}
	if score > 0 {
		member := redis.Z{
			Score:  float64(runAt),
			Member: key,
		}
		_, err := redisdb.Instance().ZAdd(KEY, member).Result()
		return err
	}
	return nil
}

func (t *TimerTask) del(key string) error {
	_, err := redisdb.Instance().Del(mfa_key(key)).Result()
	if err != nil {
		return err
	}
	_, err = redisdb.Instance().ZRem(KEY, key).Result()
	return err
}

type TickerTaskParams struct{}

func (t *TimerTask) tickerTask() {
	opt := redis.ZRangeBy{
		Min:    "0",
		Max:    strconv.Itoa(int(time.Now().Unix())),
		Offset: 0,
		Count:  gosconf.TIMERTASK_TASKS_PER_CHECK,
	}
	members, err := redisdb.Instance().ZRangeByScoreWithScores(KEY, opt).Result()
	if err != nil {
		logger.ERR("tickerTask failed: ", err)
		return
	}
	for _, member := range members {
		key := member.Member.(string)
		redisdb.Instance().ZRem(KEY, key)
		t.pool.ProcessAsync(key)
	}
}

func (t *TimerTask) handleTask(key string) error {
	mfa_key := mfa_key(key)
	content, err := redisdb.Instance().Get(mfa_key).Result()
	if err == redis.Nil {
		return nil
	}
	if err != nil {
		return err
	}
	redisdb.Instance().Del(mfa_key)
	chunks := strings.Split(content, ":")
	_, err = player_rpc.RpcPlayerRaw(chunks[0], []byte(chunks[1]))
	return err
}
