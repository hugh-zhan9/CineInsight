package database

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrMaintenance = errors.New("database is in maintenance mode")

type databaseOperationGate struct {
	mu       sync.Mutex
	cond     *sync.Cond
	active   bool
	inFlight int
}

var operationGate = newDatabaseOperationGate()

type maintenanceAccessKey struct{}

// WithMaintenanceAccess is reserved for the restore lifecycle after external
// access has been fenced. It lets that lifecycle persist its own outcome while
// normal GORM calls continue to fail with ErrMaintenance.
func WithMaintenanceAccess(db *gorm.DB) *gorm.DB {
	ctx := context.WithValue(context.Background(), maintenanceAccessKey{}, true)
	return db.WithContext(ctx)
}

func hasMaintenanceAccess(tx *gorm.DB) bool {
	return tx != nil && tx.Statement != nil && tx.Statement.Context != nil && tx.Statement.Context.Value(maintenanceAccessKey{}) == true
}

func newDatabaseOperationGate() *databaseOperationGate {
	gate := &databaseOperationGate{}
	gate.cond = sync.NewCond(&gate.mu)
	return gate
}

func (gate *databaseOperationGate) enter() error {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if gate.active {
		return ErrMaintenance
	}
	gate.inFlight++
	return nil
}

func (gate *databaseOperationGate) leave() {
	gate.mu.Lock()
	gate.inFlight--
	if gate.inFlight == 0 {
		gate.cond.Broadcast()
	}
	gate.mu.Unlock()
}

func (gate *databaseOperationGate) beginMaintenance() func() {
	gate.mu.Lock()
	gate.active = true
	for gate.inFlight > 0 {
		gate.cond.Wait()
	}
	gate.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			gate.mu.Lock()
			gate.active = false
			gate.cond.Broadcast()
			gate.mu.Unlock()
		})
	}
}

func (gate *databaseOperationGate) isActive() bool {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	return gate.active
}

// BeginMaintenance rejects new database operations and waits for operations
// already registered with the gate to finish. The returned release function
// must only be called when the existing database is safe to use again.
func BeginMaintenance() func() {
	return operationGate.beginMaintenance()
}

// MaintenanceActive 报告维护围栏当前是否生效（恢复备份或切换后端期间）。
// 只用于让自动任务在围栏期间跳过这一轮（D-PC56 定时备份），不用于判断能否写入：
// 判断与写入之间围栏随时可能生效，真正的拒绝由回调里的 enter 负责。
func MaintenanceActive() bool {
	return operationGate.isActive()
}

// migrationMarkerTable 是 database/migrator 在目标库里写的迁移标记表
// （migrator.migrationMarker.TableName）。migrator 依赖本包，这里不能反向引用它，
// 只能写同一个字面量；TestAPP02ClearApplicationTables… 用 migrator.Preflight 钉住两边一致。
const migrationMarkerTable = "cineinsight_migration_marker"

// semanticVectorTables 是本应用用原始 DDL 建的语义向量表（仅 PostgreSQL + pgvector，见
// semantic_vector.go / image_semantic_vector.go），不在 AllModels() 里。目标库若曾经作为
// 活动库建过语义索引，这两张表的外键指向 videos / images，不先删它们主表就删不掉。
var semanticVectorTables = []string{"video_semantic_vectors", imageSemanticVectorsTable}

// ApplicationTables 返回 DropApplicationTables 的删除范围，按删除顺序排列：
//  1. 语义向量表——同样有外键指向主表；
//  2. AllModels() 里模型声明的隐式多对多关联表（video_tags、image_tags）——它们有外键
//     指向主表，不先删的话 PostgreSQL 会以「有其他对象依赖」拒绝删主表；
//  3. AllModels() 逆序——清单是拓扑有序的（被引用者在前），逆序即引用方先删；
//  4. 迁移器的完成标记表——不删它，半迁移的目标库会一直被 Preflight 判成「残留」而无法重试。
//
// 范围只包含本应用自己建的表，不按前缀或 information_schema 猜，目标库里的其他表不在其中。
func ApplicationTables(db *gorm.DB) ([]string, error) {
	all := models.AllModels()
	tables := make([]string, 0, len(all))
	joins := make([]string, 0, 2)
	seenJoin := map[string]bool{}
	for _, model := range all {
		stmt := &gorm.Statement{DB: db}
		if err := stmt.Parse(model); err != nil {
			return nil, fmt.Errorf("解析模型 %T 失败: %w", model, err)
		}
		tables = append(tables, stmt.Schema.Table)
		for _, relation := range stmt.Schema.Relationships.Many2Many {
			if relation.JoinTable == nil || seenJoin[relation.JoinTable.Table] {
				continue
			}
			seenJoin[relation.JoinTable.Table] = true
			joins = append(joins, relation.JoinTable.Table)
		}
	}
	ordered := make([]string, 0, len(semanticVectorTables)+len(joins)+len(tables)+1)
	ordered = append(ordered, semanticVectorTables...)
	ordered = append(ordered, joins...)
	for index := len(tables) - 1; index >= 0; index-- {
		ordered = append(ordered, tables[index])
	}
	return append(ordered, migrationMarkerTable), nil
}

// DropApplicationTables 在一个事务里对 ApplicationTables 逐张执行 DROP TABLE IF EXISTS，
// 返回实际存在并被删掉的表（D-PC55「清空目标库」的 PostgreSQL 分支）。
//
// 只给切换失败后的**目标库**用：db 必须是临时打开的目标连接，绝不能是当前活动库，
// 这里对 DB 本身做一次防呆。刻意不带 CASCADE（GORM 的 PostgreSQL DropTable 会带）：
// 目标库里若有本应用之外的对象依赖本应用的表，CASCADE 会顺手改掉那些对象；不带则整个
// 事务失败回滚，一张表都不删，由调用方把原因报给用户。
func DropApplicationTables(db *gorm.DB) ([]string, error) {
	if db == nil {
		return nil, errors.New("目标库未连接")
	}
	if DB != nil && db == DB {
		return nil, errors.New("不能清空当前正在使用的数据库")
	}
	tables, err := ApplicationTables(db)
	if err != nil {
		return nil, err
	}
	dropped := make([]string, 0, len(tables))
	err = db.Transaction(func(tx *gorm.DB) error {
		for _, table := range tables {
			if !tx.Migrator().HasTable(table) {
				continue
			}
			if err := tx.Exec("DROP TABLE IF EXISTS ?", clause.Table{Name: table}).Error; err != nil {
				return fmt.Errorf("删除表 %s 失败: %w", table, err)
			}
			dropped = append(dropped, table)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return dropped, nil
}

// Transaction keeps the maintenance gate for the full lifetime of an explicit
// GORM transaction. Statement callbacks alone cannot cover the gaps between
// statements in a user-managed transaction.
func Transaction(fn func(tx *gorm.DB) error) error {
	if err := operationGate.enter(); err != nil {
		return err
	}
	defer operationGate.leave()
	return DB.Transaction(fn)
}

const maintenanceGateToken = "cineinsight:database-maintenance-gate"

func registerMaintenanceCallbacks(db *gorm.DB) error {
	before := func(tx *gorm.DB) {
		if hasMaintenanceAccess(tx) {
			return
		}
		if err := operationGate.enter(); err != nil {
			tx.AddError(err)
			return
		}
		tx.InstanceSet(maintenanceGateToken, true)
	}
	after := func(tx *gorm.DB) {
		if acquired, ok := tx.InstanceGet(maintenanceGateToken); ok && acquired == true {
			tx.InstanceSet(maintenanceGateToken, false)
			operationGate.leave()
		}
	}

	registrations := []error{
		db.Callback().Create().Before("gorm:begin_transaction").Register("cineinsight:maintenance_before_create", before),
		db.Callback().Create().After("gorm:commit_or_rollback_transaction").Register("cineinsight:maintenance_after_create", after),
		db.Callback().Update().Before("gorm:begin_transaction").Register("cineinsight:maintenance_before_update", before),
		db.Callback().Update().After("gorm:commit_or_rollback_transaction").Register("cineinsight:maintenance_after_update", after),
		db.Callback().Delete().Before("gorm:begin_transaction").Register("cineinsight:maintenance_before_delete", before),
		db.Callback().Delete().After("gorm:commit_or_rollback_transaction").Register("cineinsight:maintenance_after_delete", after),
		db.Callback().Query().Before("gorm:query").Register("cineinsight:maintenance_before_query", before),
		db.Callback().Query().After("gorm:after_query").Register("cineinsight:maintenance_after_query", after),
		db.Callback().Raw().Before("gorm:raw").Register("cineinsight:maintenance_before_raw", before),
		db.Callback().Raw().After("gorm:raw").Register("cineinsight:maintenance_after_raw", after),
		db.Callback().Row().Before("gorm:row").Register("cineinsight:maintenance_before_row", before),
		db.Callback().Row().After("gorm:row").Register("cineinsight:maintenance_after_row", after),
	}
	return errors.Join(registrations...)
}
