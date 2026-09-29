package services

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// LIB-10 全局守卫（详细设计 §3.1、D-PC06）：videos 表上每一处写 is_stale 的地方都必须同时写入
// 或清空 stale_reason——置 true 时带上非空原因，置 false 时把原因清成空串。
//
// 守卫按源码静态检查（go/ast），扫描模块里全部非测试 Go 文件（根包、cmd、database、internal、
// models、services）。认作「写入点」的形式：
//   - map 字面量里的 "is_stale" 键（Updates / UpdateColumns 的载荷，或先建好再传入的 map）；
//   - 对 map 变量的下标赋值 m["is_stale"] = …；
//   - Update / UpdateColumn("is_stale", …) 单列更新——它不可能同时写原因，对视频一律不许；
//   - 原始 SQL 字符串里 UPDATE … SET … is_stale；
//   - models.Video 结构体字面量里的 IsStale 字段（Create 的载荷）。
//
// 目标是图片表的写入不受约束：images 没有 stale_reason 列，图片的隐藏原因按路径与根的在线
// 状态推算、不落库（§3.1「图片扫描隐藏」）。目标表从调用链上的 Model(…) / Table(…) 推断；
// 推断不出来的一律按视频处理（从严），真要写图片就把 Model(&models.Image{}) 写明白。
//
// 不算写入点的：Where / Select / Order 里的 is_stale（只读），以及对内存结构体字段的赋值
// （video.IsStale = …）——后者不落库，内存与库的一致性由各自的用例负责。

var staleGuardScanRoots = []struct {
	dir       string
	recursive bool
}{
	{".", false},
	{"cmd", true},
	{"database", true},
	{"internal", true},
	{"models", true},
	{"services", true},
}

// staleGuardUpdateSQL 认出原始 SQL 里的 UPDATE … SET … is_stale。
var staleGuardUpdateSQL = regexp.MustCompile(`(?is)\bupdate\b.*\bset\b.*\bis_stale\b`)
var staleGuardUpdateImagesSQL = regexp.MustCompile(`(?is)\bupdate\s+"?images"?\b`)

type staleGuardSite struct {
	pos       token.Position
	kind      string
	violation string
}

func TestLIB10EveryIsStaleWriteMaintainsStaleReason(t *testing.T) {
	moduleRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(moduleRoot, "go.mod")); err != nil {
		t.Fatalf("找不到模块根目录 %s: %v", moduleRoot, err)
	}
	var files []string
	for _, root := range staleGuardScanRoots {
		dir := filepath.Join(moduleRoot, root.dir)
		if !root.recursive {
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if staleGuardSourceFile(entry.Name()) && !entry.IsDir() {
					files = append(files, filepath.Join(dir, entry.Name()))
				}
			}
			continue
		}
		err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if name := entry.Name(); path != dir && (strings.HasPrefix(name, ".") || name == "testdata") {
					return filepath.SkipDir
				}
				return nil
			}
			if staleGuardSourceFile(entry.Name()) {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(files)

	videoSites, imageSites := 0, 0
	var violations []string
	for _, path := range files {
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		sites, err := staleGuardCheckSource(path, source)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", path, err)
		}
		for _, site := range sites {
			if site.kind == "image" {
				imageSites++
				continue
			}
			videoSites++
			if site.violation != "" {
				rel, _ := filepath.Rel(moduleRoot, site.pos.Filename)
				violations = append(violations, rel+":"+strconv.Itoa(site.pos.Line)+": "+site.violation)
			}
		}
	}
	// 守卫要真的扫到东西：扫描根配错或写入形式整体换了写法时，零命中会让它永远是绿的。
	if videoSites < 5 || imageSites < 3 {
		t.Fatalf("守卫只找到 %d 处视频写入、%d 处图片写入，扫描范围或识别规则可能失效", videoSites, imageSites)
	}
	if len(violations) > 0 {
		t.Fatalf("以下写 is_stale 的地方没有同时维护 stale_reason（详细设计 §3.1）：\n%s", strings.Join(violations, "\n"))
	}
	t.Logf("检查了 %d 个文件：视频表写入 %d 处，图片表写入 %d 处（不受约束）", len(files), videoSites, imageSites)
}

// TestLIB10StaleReasonGuardCatchesMutations 证明守卫能抓住故意漏写的变异：每个用例是一段
// 最小源码，前几条取自真实写法（markVideoStale、clearVideoStale、播放成功清失效）再改坏一处。
func TestLIB10StaleReasonGuardCatchesMutations(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		violations int
		imageSites int
	}{
		{"标失效漏写原因", `func f(id uint) { database.DB.Model(&models.Video{}).Where("id = ? AND is_stale = ?", id, false).Updates(map[string]interface{}{"is_stale": true}) }`, 1, 0},
		{"清失效漏清原因", `func f(id uint) { database.DB.Model(&models.Video{}).Where("id = ?", id).Updates(map[string]interface{}{"is_stale": false, "path": "p"}) }`, 1, 0},
		{"清失效却写了原因", `func f(id uint) { database.DB.Model(&models.Video{}).Where("id = ?", id).Updates(map[string]interface{}{"is_stale": false, "stale_reason": "missing_file"}) }`, 1, 0},
		{"标失效却写空原因", `func f(id uint) { database.DB.Model(&models.Video{}).Where("id = ?", id).Updates(map[string]interface{}{"is_stale": true, "stale_reason": ""}) }`, 1, 0},
		{"视频单列更新", `func f(tx *gorm.DB, id uint) { tx.Model(&models.Video{}).Where("id = ?", id).Update("is_stale", true) }`, 1, 0},
		{"视频参数单列更新", `func f(tx *gorm.DB, video *models.Video) { tx.Model(video).Update("is_stale", false) }`, 1, 0},
		{"推断不出目标按视频从严", `func f(tx *gorm.DB, name string) { tx.Table(name).Updates(map[string]interface{}{"is_stale": true}) }`, 1, 0},
		{"先建 map 再补键", `func f(now int) { updates := map[string]interface{}{"last_played_at": now}; updates["is_stale"] = false; database.DB.Model(&models.Video{}).Updates(updates) }`, 1, 0},
		{"原始 SQL", "func f(id uint) { database.DB.Exec(`UPDATE videos SET is_stale = true WHERE id = ?`, id) }", 1, 0},
		{"建行结构体", `func f(p string) { database.DB.Create(&models.Video{Path: p, IsStale: true}) }`, 1, 0},

		{"成对写入", `func f(id uint, reason string) { database.DB.Model(&models.Video{}).Where("id = ?", id).Updates(map[string]interface{}{"is_stale": true, "stale_reason": reason}) }`, 0, 0},
		{"成对清空", `func f(tx *gorm.DB, video models.Video) { tx.Model(&video).Updates(map[string]interface{}{"is_stale": false, "stale_reason": ""}) }`, 0, 0},
		{"先建 map 再成对补键", `func f(now int) { updates := map[string]interface{}{"last_played_at": now}; updates["is_stale"] = false; updates["stale_reason"] = ""; database.DB.Model(&models.Video{}).Updates(updates) }`, 0, 0},
		{"结构体带原因", `func f(p string) { database.DB.Create(&models.Video{Path: p, IsStale: true, StaleReason: models.StaleReasonMissingFile}) }`, 0, 0},
		{"图片单列更新", `func f(id uint) { database.DB.Model(&models.Image{}).Where("id = ?", id).Update("is_stale", true) }`, 0, 1},
		{"图片参数", `func f(tx *gorm.DB, image *models.Image) { tx.Model(image).Updates(map[string]interface{}{"deleted_by": "user", "is_stale": false}) }`, 0, 1},
		{"图片表原始 SQL", "func f(id uint) { database.DB.Exec(`UPDATE images SET is_stale = false WHERE id = ?`, id) }", 0, 1},
		{"只读条件不算写入", `func f() { database.DB.Model(&models.Video{}).Where("is_stale = ?", false).Select("id", "is_stale").Find(&[]models.Video{}) }`, 0, 0},
		{"内存字段不算写入", `func f(video *models.Video) { video.IsStale = false }`, 0, 0},
	}
	for _, tc := range cases {
		source := []byte("package services\n\n" + tc.body + "\n")
		sites, err := staleGuardCheckSource("case.go", source)
		if err != nil {
			t.Fatalf("%s：解析失败: %v", tc.name, err)
		}
		violations, images := 0, 0
		for _, site := range sites {
			if site.kind == "image" {
				images++
			} else if site.violation != "" {
				violations++
			}
		}
		if violations != tc.violations || images != tc.imageSites {
			t.Errorf("%s：期望 %d 处违规、%d 处图片写入，实际 %d、%d（%+v）", tc.name, tc.violations, tc.imageSites, violations, images, sites)
		}
	}
}

func staleGuardSourceFile(name string) bool {
	return strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")
}

// staleGuardCheckSource 找出一个文件里全部 is_stale 写入点，并给每一处判定是否违规。
func staleGuardCheckSource(path string, source []byte) ([]staleGuardSite, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, source, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var sites []staleGuardSite
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		sites = append(sites, staleGuardCheckFunc(fset, fn)...)
	}
	// 包级变量里的 SQL 常量与 map 也算（不在任何函数体里）。
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		scope := newStaleGuardScope()
		ast.Inspect(gen, func(node ast.Node) bool {
			sites = append(sites, scope.siteAt(fset, node, nil)...)
			return true
		})
	}
	return sites, nil
}

// staleGuardScope 是一个函数里按名字记下的变量类型（只关心 models.Video / models.Image）与
// map 变量的键。同名变量在不同作用域里类型不一致时记为未知，退回从严。
type staleGuardScope struct {
	models   map[string]string
	mapKeys  map[string]map[string]bool
	indexSet map[string]map[string]bool
}

func newStaleGuardScope() *staleGuardScope {
	return &staleGuardScope{models: map[string]string{}, mapKeys: map[string]map[string]bool{}, indexSet: map[string]map[string]bool{}}
}

func (s *staleGuardScope) record(name, model string) {
	if name == "" || name == "_" {
		return
	}
	if existing, ok := s.models[name]; ok && existing != model {
		s.models[name] = ""
		return
	}
	s.models[name] = model
}

func staleGuardCheckFunc(fset *token.FileSet, fn *ast.FuncDecl) []staleGuardSite {
	scope := newStaleGuardScope()
	recordFields := func(fields *ast.FieldList) {
		if fields == nil {
			return
		}
		for _, field := range fields.List {
			model := staleGuardTypeModel(field.Type)
			for _, name := range field.Names {
				scope.record(name.Name, model)
			}
		}
	}
	recordFields(fn.Recv)
	recordFields(fn.Type.Params)
	parents := map[ast.Node]ast.Node{}
	var stack []ast.Node
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		if node == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		if len(stack) > 0 {
			parents[node] = stack[len(stack)-1]
		}
		stack = append(stack, node)
		switch n := node.(type) {
		case *ast.FuncLit:
			recordFields(n.Type.Params)
		case *ast.ValueSpec:
			for index, name := range n.Names {
				model := staleGuardTypeModel(n.Type)
				if model == "" && index < len(n.Values) {
					model = staleGuardExprModel(n.Values[index], scope)
				}
				scope.record(name.Name, model)
				if index < len(n.Values) {
					scope.recordMapLiteral(name.Name, n.Values[index])
				}
			}
		case *ast.AssignStmt:
			for index, lhs := range n.Lhs {
				if index >= len(n.Rhs) {
					break
				}
				if ident, ok := lhs.(*ast.Ident); ok {
					if n.Tok == token.DEFINE {
						scope.record(ident.Name, staleGuardExprModel(n.Rhs[index], scope))
					}
					scope.recordMapLiteral(ident.Name, n.Rhs[index])
				}
				if idx, ok := lhs.(*ast.IndexExpr); ok {
					if ident, ok := idx.X.(*ast.Ident); ok {
						if key, ok := staleGuardStringLit(idx.Index); ok {
							if scope.indexSet[ident.Name] == nil {
								scope.indexSet[ident.Name] = map[string]bool{}
							}
							scope.indexSet[ident.Name][key] = true
						}
					}
				}
			}
		case *ast.RangeStmt:
			if value, ok := n.Value.(*ast.Ident); ok {
				if slice, ok := n.X.(*ast.Ident); ok {
					scope.record(value.Name, scope.models[slice.Name])
				}
			}
		}
		return true
	})
	var sites []staleGuardSite
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		sites = append(sites, scope.siteAt(fset, node, parents)...)
		return true
	})
	return sites
}

func (s *staleGuardScope) recordMapLiteral(name string, value ast.Expr) {
	lit, ok := value.(*ast.CompositeLit)
	if !ok {
		return
	}
	if _, ok := lit.Type.(*ast.MapType); !ok {
		return
	}
	keys := s.mapKeys[name]
	if keys == nil {
		keys = map[string]bool{}
		s.mapKeys[name] = keys
	}
	for _, elt := range lit.Elts {
		if kv, ok := elt.(*ast.KeyValueExpr); ok {
			if key, ok := staleGuardStringLit(kv.Key); ok {
				keys[key] = true
			}
		}
	}
}

// siteAt 判断 node 是不是一处写入点；是的话给出它的目标与判定。
func (s *staleGuardScope) siteAt(fset *token.FileSet, node ast.Node, parents map[ast.Node]ast.Node) []staleGuardSite {
	switch n := node.(type) {
	case *ast.CompositeLit:
		if _, ok := n.Type.(*ast.MapType); ok {
			values := staleGuardMapValues(n)
			value, ok := values["is_stale"]
			if !ok {
				return nil
			}
			model := ""
			if call, ok := parents[n].(*ast.CallExpr); ok && staleGuardIsUpdatesCall(call) {
				model = staleGuardChainModel(call.Fun, s)
			}
			return []staleGuardSite{s.judge(fset, n.Pos(), model, "map", value, values)}
		}
		if _, ok := n.Type.(*ast.ArrayType); ok {
			return nil
		}
		switch staleGuardTypeModel(n.Type) {
		case "Video":
			values := map[string]ast.Expr{}
			for _, elt := range n.Elts {
				if kv, ok := elt.(*ast.KeyValueExpr); ok {
					if key, ok := kv.Key.(*ast.Ident); ok {
						values[key.Name] = kv.Value
					}
				}
			}
			value, ok := values["IsStale"]
			if !ok {
				return nil
			}
			renamed := map[string]ast.Expr{}
			if reason, ok := values["StaleReason"]; ok {
				renamed["stale_reason"] = reason
			}
			return []staleGuardSite{s.judge(fset, n.Pos(), "Video", "struct", value, renamed)}
		}
	case *ast.AssignStmt:
		var sites []staleGuardSite
		for _, lhs := range n.Lhs {
			idx, ok := lhs.(*ast.IndexExpr)
			if !ok {
				continue
			}
			key, ok := staleGuardStringLit(idx.Index)
			if !ok || key != "is_stale" {
				continue
			}
			// 目标表要等 map 传进 Updates 才知道，这里一律按视频从严。
			site := staleGuardSite{pos: fset.Position(idx.Pos()), kind: "video"}
			ident, ok := idx.X.(*ast.Ident)
			if !ok || !(s.indexSet[ident.Name]["stale_reason"] || s.mapKeys[ident.Name]["stale_reason"]) {
				site.violation = "对 map 补了 is_stale 却没在同一函数里补 stale_reason"
			}
			sites = append(sites, site)
		}
		return sites
	case *ast.CallExpr:
		sel, ok := n.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "Update" && sel.Sel.Name != "UpdateColumn") || len(n.Args) == 0 {
			return nil
		}
		if key, ok := staleGuardStringLit(n.Args[0]); !ok || key != "is_stale" {
			return nil
		}
		if staleGuardChainModel(n.Fun, s) == "Image" {
			return []staleGuardSite{{pos: fset.Position(n.Pos()), kind: "image"}}
		}
		return []staleGuardSite{{pos: fset.Position(n.Pos()), kind: "video", violation: sel.Sel.Name + `("is_stale", …) 单列更新无法同时维护 stale_reason，改用 Updates 成对写入`}}
	case *ast.BasicLit:
		if n.Kind != token.STRING {
			return nil
		}
		text, err := strconv.Unquote(n.Value)
		if err != nil || !staleGuardUpdateSQL.MatchString(text) {
			return nil
		}
		if staleGuardUpdateImagesSQL.MatchString(text) {
			return []staleGuardSite{{pos: fset.Position(n.Pos()), kind: "image"}}
		}
		site := staleGuardSite{pos: fset.Position(n.Pos()), kind: "video"}
		if !strings.Contains(text, "stale_reason") {
			site.violation = "原始 SQL 写 is_stale 却没写 stale_reason"
		}
		return []staleGuardSite{site}
	}
	return nil
}

// judge 按目标与取值判定一处 map / 结构体写入：视频必须带 stale_reason，false 配空串、true 配非空。
func (s *staleGuardScope) judge(fset *token.FileSet, pos token.Pos, model, form string, value ast.Expr, values map[string]ast.Expr) staleGuardSite {
	if model == "Image" {
		return staleGuardSite{pos: fset.Position(pos), kind: "image"}
	}
	site := staleGuardSite{pos: fset.Position(pos), kind: "video"}
	reason, ok := values["stale_reason"]
	if !ok {
		site.violation = form + " 写了 is_stale 却没有 stale_reason"
		return site
	}
	literal, isLiteral := staleGuardStringLit(reason)
	if ident, ok := value.(*ast.Ident); ok {
		switch {
		case ident.Name == "false" && !(isLiteral && literal == ""):
			site.violation = "is_stale=false 时 stale_reason 必须清成空串"
		case ident.Name == "true" && isLiteral && literal == "":
			site.violation = "is_stale=true 时 stale_reason 不能为空"
		}
	}
	return site
}

func staleGuardMapValues(lit *ast.CompositeLit) map[string]ast.Expr {
	values := map[string]ast.Expr{}
	for _, elt := range lit.Elts {
		if kv, ok := elt.(*ast.KeyValueExpr); ok {
			if key, ok := staleGuardStringLit(kv.Key); ok {
				values[key] = kv.Value
			}
		}
	}
	return values
}

func staleGuardIsUpdatesCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && (sel.Sel.Name == "Updates" || sel.Sel.Name == "UpdateColumns")
}

// staleGuardChainModel 沿 GORM 调用链往回找 Model(…) 或 Table("…")，推断写入的是哪张表。
func staleGuardChainModel(expr ast.Expr, scope *staleGuardScope) string {
	for {
		sel, ok := expr.(*ast.SelectorExpr)
		if !ok {
			return ""
		}
		call, ok := sel.X.(*ast.CallExpr)
		if !ok {
			return ""
		}
		if inner, ok := call.Fun.(*ast.SelectorExpr); ok && len(call.Args) > 0 {
			switch inner.Sel.Name {
			case "Model":
				return staleGuardExprModel(call.Args[0], scope)
			case "Table":
				if table, ok := staleGuardStringLit(call.Args[0]); ok {
					if fields := strings.Fields(table); len(fields) > 0 {
						switch fields[0] {
						case "videos":
							return "Video"
						case "images":
							return "Image"
						}
					}
				}
				return ""
			}
		}
		expr = call.Fun
	}
}

func staleGuardExprModel(expr ast.Expr, scope *staleGuardScope) string {
	switch e := expr.(type) {
	case *ast.UnaryExpr:
		return staleGuardExprModel(e.X, scope)
	case *ast.ParenExpr:
		return staleGuardExprModel(e.X, scope)
	case *ast.CompositeLit:
		return staleGuardTypeModel(e.Type)
	case *ast.CallExpr:
		if ident, ok := e.Fun.(*ast.Ident); ok && ident.Name == "new" && len(e.Args) == 1 {
			return staleGuardTypeModel(e.Args[0])
		}
	case *ast.Ident:
		return scope.models[e.Name]
	case *ast.IndexExpr:
		if ident, ok := e.X.(*ast.Ident); ok {
			return scope.models[ident.Name]
		}
	}
	return ""
}

// staleGuardTypeModel 从类型表达式里取出 models.Video / models.Image（含指针与切片）。
func staleGuardTypeModel(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.StarExpr:
		return staleGuardTypeModel(e.X)
	case *ast.ArrayType:
		return staleGuardTypeModel(e.Elt)
	case *ast.SelectorExpr:
		if pkg, ok := e.X.(*ast.Ident); ok && pkg.Name == "models" && (e.Sel.Name == "Video" || e.Sel.Name == "Image") {
			return e.Sel.Name
		}
	case *ast.Ident:
		// models 包自己的代码里直接写 Video / Image。
		if e.Name == "Video" || e.Name == "Image" {
			return e.Name
		}
	}
	return ""
}

func staleGuardStringLit(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return value, true
}
