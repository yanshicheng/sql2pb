package core

import (
	"bytes"
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/chuckpreslar/inflect"
	"github.com/serenize/snaker"
	"github.com/yanshicheng/sql2pb/tools/stringx"
)

const (
	// proto3 语法类型
	proto3 = "proto3"

	// indent 字段缩进，protobuf 风格建议两个空格
	indent = "  "

	// 生成 protobuf 字段风格
	fieldStyleToCamelWithStartLower = "sqlPb"
	fieldStyleToSnake               = "sql_pb"
)

// 忽略字段配置
var (
	allIgnoreFields     = []string{"del_state", "delete_at", "delete_time", "deleted_at", "is_deleted"}
	defaultIgnoreFields = []string{"del_state", "delete_time", "delete_at", "deleted_at", "is_deleted"}
	updateIgnoreFields  = []string{"create_time", "createBy", "create_by", "create_at", "update_time", "update_at", "del_state", "delete_at", "delete_time", "deleteAt", "updateAt", "createAt", "deleted_at", "created_at", "updated_at", "is_deleted", "created_by"}
	addReqIgnoreFields  = []string{"id", "create_time", "create_at", "update_at", "update_time", "del_state", "delete_at", "delete_time", "deleteAt", "updateAt", "createAt", "deleted_at", "created_at", "updated_at", "is_deleted", "updated_by"}
	searchIgnoreFields  = []string{"id", "create_time", "create_at", "update_time", "update_at", "del_state", "delete_at", "delete_time", "deleteAt", "updateAt", "createAt", "deleted_at", "created_at", "updated_at", "created_by", "create_by", "is_deleted"}
)

// 忽略字段 map，用于快速查找
var (
	defaultIgnoreFieldsMap = toSet(defaultIgnoreFields)
	updateIgnoreFieldsMap  = toSet(updateIgnoreFields)
	addReqIgnoreFieldsMap  = toSet(addReqIgnoreFields)
	searchIgnoreFieldsMap  = toSet(searchIgnoreFields)
)

// toSet 将 slice 转换为 map，用于 O(1) 查找
func toSet(slice []string) map[string]struct{} {
	m := make(map[string]struct{}, len(slice))
	for _, s := range slice {
		m[s] = struct{}{}
	}
	return m
}

// isIgnored 检查字段是否在忽略列表中
func isIgnored(set map[string]struct{}, s string) bool {
	_, ok := set[s]
	return ok
}

// GenerateSchema 从数据库连接生成 protobuf schema
// 可以指定要忽略的表和列
// 返回的 schema 实现了 fmt.Stringer 接口，用于生成 protobuf 文件的字符串表示
func GenerateSchema(db *sql.DB, table string, ignoreTables, ignoreColumns []string, serviceName, goPkg, pkg, fieldStyle string) (*Schema, error) {
	s := &Schema{}

	dbs, err := dbSchema(db)
	if err != nil {
		return nil, err
	}

	s.Syntax = proto3
	s.ServiceName = serviceName
	if pkg != "" {
		s.Package = pkg
	}
	if goPkg != "" {
		s.GoPackage = goPkg
	} else {
		s.GoPackage = "./" + s.Package
	}

	cols, err := dbColumns(db, dbs, table)
	if err != nil {
		return nil, err
	}

	err = typesFromColumns(s, cols, ignoreTables, ignoreColumns, fieldStyle)
	if err != nil {
		return nil, err
	}

	sort.Sort(s.Imports)
	sort.Sort(s.Messages)
	sort.Sort(s.Enums)

	return s, nil
}

// typesFromColumns 从列集合创建 schema 属性
func typesFromColumns(s *Schema, cols []Column, ignoreTables, ignoreColumns []string, fieldStyle string) error {
	messageMap := map[string]*Message{}
	ignoreMap := map[string]bool{}
	ignoreColumnMap := map[string]bool{}
	for _, ig := range ignoreTables {
		ignoreMap[ig] = true
	}
	for _, ic := range ignoreColumns {
		ignoreColumnMap[ic] = true
	}

	for _, c := range cols {
		if _, ok := ignoreMap[c.TableName]; ok {
			continue
		}
		if _, ok := ignoreColumnMap[c.ColumnName]; ok {
			continue
		}

		messageName := snaker.SnakeToCamel(c.TableName)

		msg, ok := messageMap[messageName]
		if !ok {
			messageMap[messageName] = &Message{Name: messageName, Comment: c.TableComment, Style: fieldStyle}
			msg = messageMap[messageName]
		}

		err := parseColumn(s, msg, c)
		if err != nil {
			return err
		}
	}

	for _, v := range messageMap {
		s.Messages = append(s.Messages, v)
	}

	return nil
}

func dbSchema(db *sql.DB) (string, error) {
	var schema string
	err := db.QueryRow("SELECT SCHEMA()").Scan(&schema)
	return schema, err
}

func dbColumns(db *sql.DB, schema, table string) ([]Column, error) {
	tableArr := strings.Split(table, ",")

	q := "SELECT c.TABLE_NAME, c.COLUMN_NAME, c.IS_NULLABLE, c.DATA_TYPE, " +
		"c.CHARACTER_MAXIMUM_LENGTH, c.NUMERIC_PRECISION, c.NUMERIC_SCALE, c.COLUMN_TYPE ,c.COLUMN_COMMENT,t.TABLE_COMMENT " +
		"FROM INFORMATION_SCHEMA.COLUMNS as c  LEFT JOIN  INFORMATION_SCHEMA.TABLES as t  on c.TABLE_NAME = t.TABLE_NAME and  c.TABLE_SCHEMA = t.TABLE_SCHEMA" +
		" WHERE c.TABLE_SCHEMA = ?"

	args := []interface{}{schema}

	// 使用参数化查询，防止 SQL 注入
	if table != "" && table != "*" {
		placeholders := make([]string, len(tableArr))
		for i, t := range tableArr {
			placeholders[i] = "?"
			args = append(args, strings.TrimSpace(t))
		}
		q += " AND c.TABLE_NAME IN(" + strings.Join(placeholders, ",") + ")"
	}

	q += " ORDER BY c.TABLE_NAME, c.ORDINAL_POSITION"

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols := []Column{}

	for rows.Next() {
		cs := Column{}
		err := rows.Scan(&cs.TableName, &cs.ColumnName, &cs.IsNullable, &cs.DataType,
			&cs.CharacterMaximumLength, &cs.NumericPrecision, &cs.NumericScale, &cs.ColumnType, &cs.ColumnComment, &cs.TableComment)
		if err != nil {
			return nil, fmt.Errorf("scan column failed: %w", err)
		}

		if cs.TableComment == "" {
			cs.TableComment = stringx.From(cs.TableName).ToCamelWithStartLower()
		}

		cols = append(cols, cs)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return cols, nil
}

// Schema protobuf schema 结构
type Schema struct {
	Syntax      string
	ServiceName string
	GoPackage   string
	Package     string
	Imports     sort.StringSlice
	Messages    MessageCollection
	Enums       EnumCollection
}

// MessageCollection 可排序的 Message 集合
type MessageCollection []*Message

func (mc MessageCollection) Len() int {
	return len(mc)
}

func (mc MessageCollection) Less(i, j int) bool {
	return mc[i].Name < mc[j].Name
}

func (mc MessageCollection) Swap(i, j int) {
	mc[i], mc[j] = mc[j], mc[i]
}

// EnumCollection 可排序的 Enum 集合
type EnumCollection []*Enum

func (ec EnumCollection) Len() int {
	return len(ec)
}

func (ec EnumCollection) Less(i, j int) bool {
	return ec[i].Name < ec[j].Name
}

func (ec EnumCollection) Swap(i, j int) {
	ec[i], ec[j] = ec[j], ec[i]
}

// AppendImport 添加 import，如果已存在则跳过
func (s *Schema) AppendImport(imports string) {
	shouldAdd := true
	for _, si := range s.Imports {
		if si == imports {
			shouldAdd = false
			break
		}
	}

	if shouldAdd {
		s.Imports = append(s.Imports, imports)
	}
}

// String 返回 Schema 的字符串表示
func (s *Schema) String() string {
	buf := new(bytes.Buffer)
	buf.WriteString(fmt.Sprintf("syntax = \"%s\";\n", s.Syntax))
	buf.WriteString("\n")
	buf.WriteString(fmt.Sprintf("option go_package =\"%s\";\n", s.GoPackage))
	buf.WriteString("\n")
	buf.WriteString(fmt.Sprintf("package %s;\n", s.Package))

	buf.WriteString("\n")
	buf.WriteString("// ------------------------------------ \n")
	buf.WriteString("// Messages and Services\n")
	buf.WriteString("// ------------------------------------ \n\n")

	for _, m := range s.Messages {
		buf.WriteString("//--------------------------------" + m.Comment + "--------------------------------")
		buf.WriteString("\n")
		m.GenDefaultMessage(buf)
		m.GenRpcAddReqRespMessage(buf)
		m.GenRpcUpdateReqMessage(buf)
		m.GenRpcDelReqMessage(buf)
		m.GenRpcGetByIdReqMessage(buf)
		m.GenRpcSearchReqMessage(buf)

		buf.WriteString("service " + m.Name + "Service {\n")
		buf.WriteString("\t//-----------------------" + m.Comment + "----------------------- \n")
		buf.WriteString("\t rpc " + m.Name + "Add" + "(" + m.Name + "AddReq) returns (" + m.Name + "AddResp);\n")
		buf.WriteString("\t rpc " + m.Name + "Update" + "(" + m.Name + "UpdateReq) returns (" + m.Name + "UpdateResp);\n")
		buf.WriteString("\t rpc " + m.Name + "Del" + "(" + m.Name + "DelReq) returns (" + m.Name + "DelResp);\n")
		buf.WriteString("\t rpc " + m.Name + "GetById(" + m.Name + "GetByIdReq) returns (" + m.Name + "GetByIdResp);\n")
		buf.WriteString("\t rpc " + m.Name + "Search" + "(" + m.Name + "SearchReq) returns (" + m.Name + "SearchResp);\n")
		buf.WriteString("}\n\n")
	}

	if len(s.Enums) > 0 {
		buf.WriteString("// ------------------------------------ \n")
		buf.WriteString("// Enums\n")
		buf.WriteString("// ------------------------------------ \n\n")

		for _, e := range s.Enums {
			buf.WriteString(fmt.Sprintf("%s\n", e))
		}
	}

	return buf.String()
}

// Enum protobuf 枚举类型
type Enum struct {
	Name    string
	Comment string
	Fields  []EnumField
}

// String 返回 Enum 的字符串表示
func (e *Enum) String() string {
	buf := new(bytes.Buffer)

	buf.WriteString(fmt.Sprintf("// %s \n", e.Comment))
	buf.WriteString(fmt.Sprintf("enum %s {\n", e.Name))

	for _, f := range e.Fields {
		buf.WriteString(fmt.Sprintf("%s%s;\n", indent, f))
	}

	buf.WriteString("}\n")

	return buf.String()
}

// AppendField 添加枚举字段，如果 tag 已存在则返回错误
func (e *Enum) AppendField(ef EnumField) error {
	for _, f := range e.Fields {
		if f.Tag() == ef.Tag() {
			return fmt.Errorf("tag `%d` is already in use by field `%s`", ef.Tag(), f.Name())
		}
	}

	e.Fields = append(e.Fields, ef)

	return nil
}

// EnumField 枚举字段
type EnumField struct {
	name string
	tag  int
}

// NewEnumField 创建枚举字段
func NewEnumField(name string, tag int) EnumField {
	name = strings.ToUpper(name)

	re := regexp.MustCompile(`([^\w]+)`)
	name = re.ReplaceAllString(name, "_")

	return EnumField{name, tag}
}

// String 返回 EnumField 的字符串表示
func (ef EnumField) String() string {
	return fmt.Sprintf("%s = %d", ef.name, ef.tag)
}

// Name 返回枚举字段名
func (ef EnumField) Name() string {
	return ef.name
}

// Tag 返回枚举字段的 tag
func (ef EnumField) Tag() int {
	return ef.tag
}

// newEnumFromStrings 从字符串切片创建枚举
func newEnumFromStrings(name, comment string, ss []string) (*Enum, error) {
	enum := &Enum{}
	enum.Name = name
	enum.Comment = comment

	for i, s := range ss {
		err := enum.AppendField(NewEnumField(s, i))
		if err != nil {
			return nil, err
		}
	}

	return enum, nil
}

// Service protobuf service
type Service struct{}

// Message protobuf message
type Message struct {
	Name    string
	Comment string
	Fields  []MessageField
	Style   string
}

// filterFields 过滤字段并转换字段名风格
func (m Message) filterFields(ignoreSet map[string]struct{}) []MessageField {
	curFields := []MessageField{}
	var filedTag int
	for _, field := range m.Fields {
		if isIgnored(ignoreSet, field.Name) {
			continue
		}
		filedTag++
		newField := MessageField{
			Typ:     field.Typ,
			Name:    field.Name,
			tag:     filedTag,
			Comment: field.Comment,
		}
		newField.Name = stringx.From(newField.Name).ToCamelWithStartLower()
		if m.Style == fieldStyleToSnake {
			newField.Name = stringx.From(newField.Name).ToSnake()
		}
		if newField.Comment == "" {
			newField.Comment = newField.Name
		}
		curFields = append(curFields, newField)
	}
	return curFields
}

// GenDefaultMessage 生成默认 Message
func (m Message) GenDefaultMessage(buf *bytes.Buffer) {
	newMsg := Message{
		Name:    m.Name,
		Comment: m.Comment,
		Style:   m.Style,
		Fields:  m.filterFields(defaultIgnoreFieldsMap),
	}
	buf.WriteString(fmt.Sprintf("%s\n", newMsg))
}

// GenRpcAddReqRespMessage 生成 Add 请求和响应 Message
func (m Message) GenRpcAddReqRespMessage(buf *bytes.Buffer) {
	// 请求
	reqMsg := Message{
		Name:    m.Name + "AddReq",
		Comment: m.Comment,
		Style:   m.Style,
		Fields:  m.filterFields(addReqIgnoreFieldsMap),
	}
	buf.WriteString(fmt.Sprintf("%s\n", reqMsg))

	// 响应
	respMsg := Message{
		Name:    m.Name + "AddResp",
		Comment: m.Comment,
		Style:   m.Style,
		Fields:  []MessageField{},
	}
	buf.WriteString(fmt.Sprintf("%s\n", respMsg))
}

// GenRpcUpdateReqMessage 生成 Update 请求和响应 Message
func (m Message) GenRpcUpdateReqMessage(buf *bytes.Buffer) {
	// 请求
	reqMsg := Message{
		Name:    m.Name + "UpdateReq",
		Comment: m.Comment,
		Style:   m.Style,
		Fields:  m.filterFields(updateIgnoreFieldsMap),
	}
	buf.WriteString(fmt.Sprintf("%s\n", reqMsg))

	// 响应
	respMsg := Message{
		Name:    m.Name + "UpdateResp",
		Comment: m.Comment,
		Style:   m.Style,
		Fields:  []MessageField{},
	}
	buf.WriteString(fmt.Sprintf("%s\n", respMsg))
}

// GenRpcDelReqMessage 生成 Del 请求和响应 Message
func (m Message) GenRpcDelReqMessage(buf *bytes.Buffer) {
	// 请求
	reqMsg := Message{
		Name:    m.Name + "DelReq",
		Comment: m.Comment,
		Style:   m.Style,
		Fields: []MessageField{
			{Name: "id", Typ: "uint64", tag: 1, Comment: "id"},
		},
	}
	buf.WriteString(fmt.Sprintf("%s\n", reqMsg))

	// 响应
	respMsg := Message{
		Name:    m.Name + "DelResp",
		Comment: m.Comment,
		Style:   m.Style,
		Fields:  []MessageField{},
	}
	buf.WriteString(fmt.Sprintf("%s\n", respMsg))
}

// GenRpcGetByIdReqMessage 生成 GetById 请求和响应 Message
func (m Message) GenRpcGetByIdReqMessage(buf *bytes.Buffer) {
	// 请求
	reqMsg := Message{
		Name:    m.Name + "GetByIdReq",
		Comment: m.Comment,
		Style:   m.Style,
		Fields: []MessageField{
			{Name: "id", Typ: "uint64", tag: 1, Comment: "id"},
		},
	}
	buf.WriteString(fmt.Sprintf("%s\n", reqMsg))

	// 响应
	firstWord := strings.ToLower(string(m.Name[0]))
	comment := stringx.From(firstWord + m.Name[1:]).ToCamelWithStartLower()
	if m.Style == fieldStyleToSnake {
		comment = stringx.From(firstWord + m.Name[1:]).ToSnake()
	}

	respMsg := Message{
		Name:    m.Name + "GetByIdResp",
		Comment: m.Comment,
		Style:   m.Style,
		Fields: []MessageField{
			{Typ: m.Name, Name: "data", tag: 1, Comment: comment},
		},
	}
	buf.WriteString(fmt.Sprintf("%s\n", respMsg))
}

// GenRpcSearchReqMessage 生成 Search 请求和响应 Message
func (m Message) GenRpcSearchReqMessage(buf *bytes.Buffer) {
	// 请求，先添加分页字段
	curFields := []MessageField{
		{Typ: "uint64", Name: "page", tag: 1, Comment: "page"},
		{Typ: "uint64", Name: "pageSize", tag: 2, Comment: "pageSize"},
		{Typ: "string", Name: "orderField", tag: 3, Comment: "orderField"},
		{Typ: "bool", Name: "isAsc", tag: 4, Comment: "isAsc"},
	}
	var filedTag = len(curFields)
	for _, field := range m.Fields {
		if isIgnored(searchIgnoreFieldsMap, field.Name) {
			continue
		}
		filedTag++
		newField := MessageField{
			Typ:     field.Typ,
			Name:    field.Name,
			tag:     filedTag,
			Comment: field.Comment,
		}
		newField.Name = stringx.From(newField.Name).ToCamelWithStartLower()
		if m.Style == fieldStyleToSnake {
			newField.Name = stringx.From(newField.Name).ToSnake()
		}
		if newField.Comment == "" {
			newField.Comment = newField.Name
		}
		curFields = append(curFields, newField)
	}

	reqMsg := Message{
		Name:    m.Name + "SearchReq",
		Comment: m.Comment,
		Style:   m.Style,
		Fields:  curFields,
	}
	buf.WriteString(fmt.Sprintf("%s\n", reqMsg))

	// 响应
	firstWord := strings.ToLower(string(m.Name[0]))
	comment := stringx.From(firstWord + m.Name[1:]).ToCamelWithStartLower()
	if m.Style == fieldStyleToSnake {
		comment = stringx.From(firstWord + m.Name[1:]).ToSnake()
	}

	respMsg := Message{
		Name:    m.Name + "SearchResp",
		Comment: m.Comment,
		Style:   m.Style,
		Fields: []MessageField{
			{Typ: "repeated " + m.Name, Name: "data", tag: 1, Comment: comment},
			{Typ: "uint64", Name: "total", tag: 2, Comment: "total"},
		},
	}
	buf.WriteString(fmt.Sprintf("%s\n", respMsg))
}

// String 返回 Message 的字符串表示
func (m Message) String() string {
	var buf bytes.Buffer

	buf.WriteString(fmt.Sprintf("message %s {\n", m.Name))
	for _, f := range m.Fields {
		buf.WriteString(fmt.Sprintf("%s%s; //%s\n", indent, f, f.Comment))
	}
	buf.WriteString("}\n")

	return buf.String()
}

// AppendField 添加 Message 字段，如果 tag 已存在则返回错误
func (m *Message) AppendField(mf MessageField) error {
	for _, f := range m.Fields {
		if f.Tag() == mf.Tag() {
			return fmt.Errorf("tag `%d` is already in use by field `%s`", mf.Tag(), f.Name)
		}
	}

	m.Fields = append(m.Fields, mf)

	return nil
}

// MessageField Message 字段
type MessageField struct {
	Typ     string
	Name    string
	tag     int
	Comment string
}

// NewMessageField 创建 Message 字段
func NewMessageField(typ, name string, tag int, comment string) MessageField {
	return MessageField{typ, name, tag, comment}
}

// Tag 返回字段的 tag
func (f MessageField) Tag() int {
	return f.tag
}

// String 返回字段的字符串表示
func (f MessageField) String() string {
	return fmt.Sprintf("%s %s = %d", f.Typ, f.Name, f.tag)
}

// Column 数据库列
type Column struct {
	Style                  string
	TableName              string
	TableComment           string
	ColumnName             string
	IsNullable             string
	DataType               string
	CharacterMaximumLength sql.NullInt64
	NumericPrecision       sql.NullInt64
	NumericScale           sql.NullInt64
	ColumnType             string
	ColumnComment          string
}

// Table 数据库表
type Table struct {
	TableName  string
	ColumnName string
}

// MySQL 类型到 protobuf 类型的映射
var mysqlToProtoType = map[string]string{
	"char":       "string",
	"varchar":    "string",
	"text":       "string",
	"longtext":   "string",
	"mediumtext": "string",
	"tinytext":   "string",
	"blob":       "bytes",
	"mediumblob": "bytes",
	"longblob":   "bytes",
	"varbinary":  "bytes",
	"binary":     "bytes",
	"date":       "int64",
	"time":       "int64",
	"datetime":   "int64",
	"timestamp":  "int64",
	"bool":       "int64",
	"bit":        "int64",
	"float":      "double",
	"decimal":    "double",
	"double":     "double",
	"json":       "string",
}

// parseColumn 解析列并插入到 Message 中
// 如果遇到枚举类型，会添加到 Schema 的 Enums 中
// 如果找不到兼容的 protobuf 类型则返回错误
func parseColumn(s *Schema, msg *Message, col Column) error {
	typ := strings.ToLower(col.DataType)
	var fieldType string

	// 先从映射表查找
	if pt, ok := mysqlToProtoType[typ]; ok {
		fieldType = pt
	}

	// 特殊类型处理
	switch typ {
	case "enum", "set":
		enumList := regexp.MustCompile(`[enum|set]\((.+?)\)`).FindStringSubmatch(col.ColumnType)
		enums := strings.FieldsFunc(enumList[1], func(c rune) bool {
			cs := string(c)
			return cs == "," || cs == "'"
		})

		enumName := inflect.Singularize(snaker.SnakeToCamel(col.TableName)) + snaker.SnakeToCamel(col.ColumnName)
		enum, err := newEnumFromStrings(enumName, col.ColumnComment, enums)
		if err != nil {
			return err
		}

		s.Enums = append(s.Enums, enum)
		fieldType = enumName

	case "tinyint", "smallint", "int", "mediumint", "bigint":
		fieldType = "int64"
		// BIGINT UNSIGNED 映射为 uint64
		if typ == "bigint" && strings.Contains(strings.ToLower(col.ColumnType), "unsigned") {
			fieldType = "uint64"
		} else if typ == "tinyint" && strings.Contains(strings.ToLower(col.ColumnType), "(1)") {
			// tinyint(1) 通常表示 bool，但这里用 int64 表示
			fieldType = "int64"
		}
	}

	// id 字段强制为 uint64
	if strings.ToLower(col.ColumnName) == "id" {
		fieldType = "uint64"
	}

	if fieldType == "" {
		return fmt.Errorf("no compatible protobuf type found for `%s`. column: `%s`.`%s`", col.DataType, col.TableName, col.ColumnName)
	}

	field := NewMessageField(fieldType, col.ColumnName, len(msg.Fields)+1, col.ColumnComment)

	err := msg.AppendField(field)
	if err != nil {
		return err
	}

	return nil
}
