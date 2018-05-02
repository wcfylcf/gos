package memstore

import (
	"database/sql"
	"github.com/go-gorp/gorp"
	_ "github.com/go-sql-driver/mysql"
	. "goslib/base_model"
	"strings"
	"time"
	"goslib/logger"
)

type Filter func(elem interface{}) bool

type Store map[string]interface{}

type TableStatus map[string]int8
type StoreStatus map[string]TableStatus

type dataLoader func(modelName string, ets *MemStore)

var dataLoaderMap = map[string]dataLoader{}

type MemStore struct {
	playerId string
	store Store
	storeStatus StoreStatus
	dataLoaded map[string]bool
	Db    *gorp.DbMap
	Ctx   interface{}
}

var sharedDBInstance *gorp.DbMap

func InitDB() {
	db, err := sql.Open("mysql", "root:@/gos_server_development")
	if err != nil {
		panic(err.Error())
	}
	sharedDBInstance = &gorp.DbMap{Db: db, Dialect: gorp.MySQLDialect{Engine: "InnoDB", Encoding: "UTF8"}}
}

func GetSharedDBInstance() *gorp.DbMap {
	return sharedDBInstance
}

func New(playerId string, ctx interface{}) *MemStore {
	e := &MemStore{
		playerId: playerId,
		store: make(Store),
		storeStatus: make(StoreStatus),
		dataLoaded: make(map[string]bool),
		Db:    GetSharedDBInstance(),
		Ctx:   ctx,
	}
	return e
}

func RegisterDataLoader(modelName string, loader dataLoader) {
	dataLoaderMap[modelName] = loader
}

func (e *MemStore) EnsureDataLoaded(modelName string) {
	if loaded, ok := e.dataLoaded[modelName]; !ok || !loaded {
		handler, ok := dataLoaderMap[modelName]
		if ok {
			handler(e.playerId, e)
		}
	}
}

func (e *MemStore) Load(namespaces []string, key string, value interface{}) {
	ctx := e.makeCtx(namespaces)
	ctx[key] = value
	e.UpdateStatus(namespaces[len(namespaces) - 1], key, STATUS_ORIGIN)
	e.dataLoaded[namespaces[len(namespaces) - 1]] = true
}

func (e *MemStore) Get(namespaces []string, key string) interface{} {
	e.EnsureDataLoaded(namespaces[len(namespaces) - 1])
	if ctx := e.getCtx(namespaces); ctx != nil {
		return ctx[key]
	} else {
		return nil
	}
}

func (e *MemStore) Set(namespaces []string, key string, value interface{}) {
	e.EnsureDataLoaded(namespaces[len(namespaces) - 1])
	ctx := e.makeCtx(namespaces)
	if ctx[key] == nil {
		e.UpdateStatus(namespaces[len(namespaces) - 1], key, STATUS_CREATE)
	} else {
		e.UpdateStatus(namespaces[len(namespaces) - 1], key, STATUS_UPDATE)
	}
	ctx[key] = value
}

func (e *MemStore) Del(namespaces []string, key string) {
	e.EnsureDataLoaded(namespaces[len(namespaces) - 1])
	if ctx := e.getCtx(namespaces); ctx != nil {
		e.UpdateStatus(namespaces[len(namespaces) - 1], key, STATUS_DELETE)
		delete(ctx, key)
	}
}

func (e *MemStore) Find(namespaces []string, filter Filter) interface{} {
	e.EnsureDataLoaded(namespaces[len(namespaces) - 1])
	if ctx := e.getCtx(namespaces); ctx != nil {
		for _, v := range ctx {
			if filter(v) {
				return v
			}
		}
	}
	return nil
}

func (e *MemStore) Select(namespaces []string, filter Filter) interface{} {
	e.EnsureDataLoaded(namespaces[len(namespaces) - 1])
	var elems []interface{}
	if ctx := e.getCtx(namespaces); ctx != nil {
		for _, v := range ctx {
			if filter(v) {
				elems = append(elems, v)
			}
		}
		return elems
	}
	return elems
}

func (e *MemStore) Count(namespaces []string) int {
	e.EnsureDataLoaded(namespaces[len(namespaces) - 1])
	if ctx := e.getCtx(namespaces); ctx != nil {
		return len(ctx)
	} else {
		return 0
	}
}

/*
 * Persist all tables in []string{"models"} namespaces
 * Example: Persist([]string{"models"})
 */
func (e *MemStore) Persist(namespaces []string) {
	sqls := make([]string, 0)
	for tableName, tableCtx := range e.getCtx(namespaces) {
		statusMap, ok := e.tableStatus(tableName)
		if ok {
			genTableSqls(sqls, statusMap, tableCtx.(Store))
		}
	}
	err := AddPersistTask(e.playerId, time.Now().Unix(), strings.Join(sqls, ";"))
	if err != nil {
		logger.ERR("AddPersitTask failed, player: ", e.playerId, " err: ", err)
		return
	}
	e.cleanStatus()
}

func (e *MemStore) getCtx(namespaces []string) Store {
	var ctx Store = nil
	for _, namespace := range namespaces {
		if ctx == nil {
			vctx, ok := e.store[namespace]
			if !ok {
				return nil
			}
			ctx = vctx.(Store)
		} else {
			vctx, ok := ctx[namespace]
			if !ok {
				return nil
			}
			ctx = vctx.(Store)
		}
	}
	return ctx
}

func (e *MemStore) makeCtx(namespaces []string) Store {
	var ctx Store = nil
	for _, namespace := range namespaces {
		if ctx == nil {
			vctx, ok := e.store[namespace]
			if !ok {
				ctx = make(Store)
				e.store[namespace] = ctx
			} else {
				ctx = vctx.(Store)
			}
		} else {
			vctx, ok := ctx[namespace]
			if !ok {
				vctx = make(Store)
				ctx[namespace] = vctx
			}
			ctx = vctx.(Store)
		}
	}
	return ctx
}

func (e *MemStore) UpdateStatus(table string, key string, status int8) {
	tableStatus, ok := e.storeStatus[table]
	if !ok {
		tableStatus = make(TableStatus)
		e.storeStatus[table] = tableStatus
	}
	tableStatus[key] = status
}

func (e *MemStore) getStatus(table string, key string) int8 {
	tableStatus, ok := e.storeStatus[table]
	if !ok {
		return STATUS_ORIGIN
	}

	status, ok := tableStatus[key]
	if !ok {
		return STATUS_ORIGIN
	}

	return status
}

/*
 * table status map[]int
 */
func (e *MemStore) tableStatus(table string) (TableStatus, bool) {
	tableStatus, ok := e.storeStatus[table]
	return tableStatus, ok
}

func (e *MemStore) cleanStatus() {
	e.storeStatus = make(StoreStatus)
}

func genTableSqls(sqls []string, statusMap TableStatus, tableCtx Store) {
	for uuid, status := range statusMap {
		model := tableCtx[uuid].(ModelInterface)
		sqls = append(sqls, model.SqlForRec(status))
	}
}
