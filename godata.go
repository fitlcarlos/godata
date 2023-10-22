package godata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/blastrain/vitess-sqlparser/sqlparser"
	go_ora "github.com/sijms/go-ora/v2"
	"reflect"
	"regexp"
	"strings"
	"time"
	"unsafe"
)

type DataSet struct {
	Connection      *Conn
	Tx              *sql.Tx
	Sql             Strings
	Fields          *Fields
	Params          *Params
	Macros          *Macros
	Rows            []Row
	Index           int
	Recno           int
	MasterSource    *MasterSource
	IndexFieldNames string
}

func NewDataSet(db *Conn) *DataSet {
	ds := &DataSet{
		Connection:   db,
		Index:        0,
		Recno:        0,
		Fields:       NewFields(),
		Params:       NewParams(),
		Macros:       NewMacros(),
		MasterSource: NewMasterSource(),
	}
	ds.Fields.Owner = ds

	return ds
}

func NewDataSetTx(tx *sql.Tx) *DataSet {
	ds := &DataSet{
		Tx:           tx,
		Index:        0,
		Recno:        0,
		Fields:       NewFields(),
		Params:       NewParams(),
		Macros:       NewMacros(),
		MasterSource: NewMasterSource(),
	}
	ds.Fields.Owner = ds

	return ds
}

func (ds *DataSet) Open() error {
	ds.Rows = nil
	ds.Index = 0
	ds.Recno = 0

	if len(ds.Fields.List) == 0 {
		ds.CreateFields()
	}

	sql := ds.GetSqlMasterDetail()

	if ds.Connection.log {
		fmt.Println(sql)
		ds.PrintParam()
	}

	rows, err := ds.Connection.DB.Query(sql, ds.GetParams()...)

	if err != nil {
		errPing := ds.Connection.DB.Ping()
		if errPing != nil {
			errConn := ds.Connection.Open()
			if errConn != nil {
				return err
			}
		} else {
			return fmt.Errorf("could not open dataset %v\n", err)
		}
	}

	defer rows.Close()

	ds.scan(rows)

	ds.First()

	return nil
}

func (ds *DataSet) OpenContext(context context.Context) error {
	ds.Rows = nil
	ds.Index = 0
	ds.Recno = 0

	if len(ds.Fields.List) == 0 {
		ds.CreateFields()
	}

	sql := ds.GetSqlMasterDetail()

	if ds.Connection.log {
		fmt.Println(sql)
		ds.PrintParam()
	}

	rows, err := ds.Connection.DB.QueryContext(context, sql, ds.GetParams()...)

	if err != nil {
		errPing := ds.Connection.DB.PingContext(context)
		if errPing != nil {
			errConn := ds.Connection.Open()
			if errConn != nil {
				return err
			}
		} else {
			return fmt.Errorf("could not open dataset %v\n", err)
		}
	}

	defer rows.Close()

	ds.scan(rows)

	ds.First()

	return nil
}

func (ds *DataSet) Close() {
	ds.Index = 0
	ds.Recno = 0
	ds.Sql.Clear()
	ds.Rows = nil
	ds.Fields = nil
	ds.Params = nil
	ds.Macros = nil
	ds.MasterSource = nil
	ds.IndexFieldNames = ""

	ds.Fields = NewFields()
	ds.Params = NewParams()
	ds.Macros = NewMacros()
	ds.MasterSource = NewMasterSource()
	ds.Fields.Owner = ds
}

func (ds *DataSet) Exec() (sql.Result, error) {
	sql := ds.GetSql()

	if ds.Connection.log {
		fmt.Println(sql)
		ds.PrintParam()
	}

	if ds.Tx != nil {
		stmt, err := ds.Tx.Prepare(sql)

		if err != nil {
			return nil, err
		}

		defer stmt.Close()

		return stmt.Exec(ds.GetParams()...)
	} else {
		stmt, err := ds.Connection.DB.Prepare(sql)

		if err != nil {
			return nil, err
		}

		defer stmt.Close()

		return stmt.Exec(ds.GetParams()...)
	}
}

func (ds *DataSet) ExecContext(context context.Context) (sql.Result, error) {
	sql := ds.GetSql()

	if ds.Connection != nil {
		if ds.Connection.log {
			fmt.Println(sql)
			ds.PrintParam()
		}
	}

	if ds.Tx != nil {
		stmt, err := ds.Tx.PrepareContext(context, sql)

		if err != nil {
			return nil, err
		}

		defer stmt.Close()

		return stmt.Exec(ds.GetParams()...)
	} else {
		stmt, err := ds.Connection.DB.PrepareContext(context, sql)

		if err != nil {
			return nil, err
		}

		defer stmt.Close()

		return stmt.ExecContext(context, ds.GetParams()...)
	}
}

func (ds *DataSet) Delete() (int64, error) {
	result, err := ds.Exec()

	if err != nil {
		return 0, err
	}

	rowsAff, err := result.RowsAffected()

	if err != nil {
		return 0, err
	}

	return rowsAff, nil
}

func (ds *DataSet) DeleteContext(context context.Context) (int64, error) {
	result, err := ds.ExecContext(context)

	if err != nil {
		return 0, err
	}

	rowsAff, err := result.RowsAffected()

	if err != nil {
		return 0, err
	}

	return rowsAff, nil
}

func (ds *DataSet) GetSql() (sql string) {

	sql = ds.Sql.Text()

	for i := 0; i < len(ds.Macros.List); i++ {
		key := ds.Macros.List[i].Name
		variant := ds.Macros.List[i].Value

		value := reflect.ValueOf(variant.Value)
		if value.Kind() == reflect.Slice || value.Kind() == reflect.Array {
			sql = strings.ReplaceAll(sql, "&"+key, JoinSlice(variant.AsValue()))
		} else {
			sql = strings.ReplaceAll(sql, "&"+key, variant.AsString())
		}
	}
	sql = strings.Replace(sql, "\r", "\n", -1)
	sql = strings.Replace(sql, "\n", "\n ", -1)
	return sql
}

func (ds *DataSet) GetSqlMasterDetail() (sql string) {

	sql = ds.GetSql()

	if ds.MasterSource.DataSource != nil {
		var sqlWhereMasterDetail string

		if len(ds.MasterSource.MasterFields) != 0 || len(ds.MasterSource.DetailFields) != 0 {
			for i := 0; i < len(ds.MasterSource.MasterFields); i++ {

				alias := ds.MasterSource.MasterFields[i] + fmt.Sprintf("%04d", i)
				if i == len(ds.MasterSource.MasterFields)-1 {
					sqlWhereMasterDetail = sqlWhereMasterDetail + ds.MasterSource.DetailFields[i] + " = :" + alias
				} else {
					sqlWhereMasterDetail = sqlWhereMasterDetail + ds.MasterSource.DetailFields[i] + " = :" + alias + " and "
				}

				ds.SetInputParam(alias, ds.MasterSource.DataSource.FieldByName(ds.MasterSource.MasterFields[i]).AsValue())
			}

			if sqlWhereMasterDetail != "" {
				sql = "select * from (" + sql + ") t where " + sqlWhereMasterDetail
			}
		} else {
			fmt.Println("MasterFields or DetailFields field cannot be empty")
		}
	}

	sql = strings.Replace(sql, "\r", "\n", -1)
	sql = strings.Replace(sql, "\n", "\n ", -1)
	return sql
}

func (ds *DataSet) GetParams() []any {
	var param []any

	for i := 0; i < len(ds.Params.List); i++ {
		key := ds.Params.List[i].Name
		value := ds.Params.List[i].Value.Value

		switch ds.Params.List[i].ParamType {
		case IN:
			param = append(param, sql.Named(key, value))
		case OUT:
			param = append(param, sql.Named(key, sql.Out{Dest: value}))
		case INOUT:
			param = append(param, sql.Named(key, sql.Out{Dest: value, In: true}))
		}
	}
	return param
}

func (ds *DataSet) GetMacros() []any {
	var macro []any
	for i := 0; i < len(ds.Macros.List); i++ {
		key := ds.Macros.List[i].Name
		value := &ds.Macros.List[i].Value.Value

		macro = append(macro, sql.Named(key, value))
	}
	return macro
}

func (ds *DataSet) scan(list *sql.Rows) {
	fieldTypes, _ := list.ColumnTypes()
	fields, _ := list.Columns()
	for list.Next() {
		columns := make([]any, len(fields))

		for i := 0; i < len(columns); i++ {
			columns[i] = &columns[i]
		}

		err := list.Scan(columns...)

		if err != nil {
			print(err)
		}

		row := NewRow()

		for i := 0; i < len(columns); i++ {
			field := ds.Fields.Add(fields[i])
			field.DataType = fieldTypes[i]
			field.Order = i + 1
			field.Index = i

			row.List[strings.ToUpper(fields[i])] = &Variant{
				Value: columns[i],
			}
		}

		ds.Rows = append(ds.Rows, row)
	}
}

func (ds *DataSet) ParamByName(paramName string) *Param {
	return ds.Params.ParamByName(paramName)
}

func (ds *DataSet) MacroByName(macroName string) *Macro {
	return ds.Macros.MacroByName(macroName)
}

func (ds *DataSet) SetInputParam(paramName string, paramValue any) *DataSet {
	ds.Params.SetInputParam(paramName, paramValue)
	return ds
}

func (ds *DataSet) SetInputParamClob(paramName string, paramValue string) *DataSet {
	ds.Params.SetInputParam(paramName, go_ora.Clob{String: paramValue})
	return ds
}

func (ds *DataSet) SetInputParamBlob(paramName string, paramValue []byte) *DataSet {
	ds.Params.SetInputParam(paramName, go_ora.Blob{Data: paramValue})
	return ds
}

func (ds *DataSet) SetOutputParam(paramName string, paramValue any) *DataSet {
	ds.Params.SetOutputParam(paramName, paramValue)
	return ds
}

func (ds *DataSet) SetOutputParamSlice(params ...ParamOut) *DataSet {
	ds.Params.SetOutputParamSlice(params...)
	return ds
}

func (ds *DataSet) SetMacro(macroName string, macroValue any) *DataSet {
	ds.Macros.SetMacro(macroName, macroValue)
	return ds
}

func (ds *DataSet) CreateFields() error {

	stmt, err := sqlparser.Parse(ds.GetSql())

	if err != nil {
		return err
	}

	sel, ok := stmt.(*sqlparser.Select)
	if ok {
		for _, expr := range sel.SelectExprs {
			_, ok := expr.(sqlparser.SelectExpr)
			if ok {
				alias, ok := expr.(*sqlparser.AliasedExpr)
				if ok {
					if !alias.As.IsEmpty() {
						_ = ds.Fields.Add(alias.As.String())
					} else {
						column, ok := alias.Expr.(*sqlparser.ColName)
						if ok {
							_ = ds.Fields.Add(column.Name.String())
						}
					}
				}
			}
		}
	}

	return nil
}

func (ds *DataSet) Prepare() {
	re := regexp.MustCompile(` :(\w+)`)
	matches := re.FindAllStringSubmatch(ds.GetSql(), -1)

	for _, match := range matches {
		paramName := match[1]

		param := &Param{
			Name:  paramName,
			Value: &Variant{Value: ""},
		}
		ds.Params.List = append(ds.Params.List, param)
	}
}

func (ds *DataSet) findFieldByName(fieldName string) *Field {
	return ds.Fields.FindFieldByName(fieldName)
}

func (ds *DataSet) FieldByName(fieldName string) *Field {
	return ds.Fields.FieldByName(fieldName)
}

func (ds *DataSet) Locate(key string, value any) bool {
	ds.First()
	for ds.Eof() == false {
		switch value.(type) {
		case string:
			if ds.FieldByName(key).AsValue() == value {
				return true
			}
		default:
			if ds.FieldByName(key).AsValue() == value {
				return true
			}
		}

		ds.Next()
	}
	return false
}

func (ds *DataSet) First() {
	ds.Index = 0
	ds.Recno = 0
	if ds.Count() > 0 {
		ds.Recno = 1
	}
}

func (ds *DataSet) Next() {
	if !ds.Eof() {
		ds.Index++
		ds.Recno++
	}
}

func (ds *DataSet) Previous() {
	if !ds.Bof() {
		ds.Index--
		ds.Recno--
	}
}

func (ds *DataSet) Last() {
	if !ds.Eof() {
		ds.Index = ds.Count()
		ds.Recno = ds.Count() + 1
	}
}

func (ds *DataSet) Bof() bool {
	return ds.Count() == 0 || ds.Recno == 1
}

func (ds *DataSet) Eof() bool {
	return ds.Count() == 0 || ds.Recno > ds.Count()
}

func (ds *DataSet) IsEmpty() bool {
	return ds.Count() == 0
}

func (ds *DataSet) IsNotEmpty() bool {
	return ds.Count() > 0
}

func (ds *DataSet) Count() int {
	return len(ds.Rows)
}

func (ds *DataSet) AddSql(sql string) *DataSet {
	ds.Sql.Add(sql)

	return ds
}

func (ds *DataSet) AddMasterSource(dataSet *DataSet) *DataSet {
	ds.MasterSource.AddMasterSource(dataSet)
	return ds
}

func (ds *DataSet) AddDetailFields(fields ...string) *DataSet {
	ds.MasterSource.AddDetailFields(fields...)
	return ds
}

func (ds *DataSet) AddMasterFields(fields ...string) *DataSet {
	ds.MasterSource.AddMasterFields(fields...)
	return ds
}

func (ds *DataSet) ClearDetailFields(fields ...string) *DataSet {
	ds.MasterSource.ClearDetailFields()
	return ds
}

func (ds *DataSet) ClearMasterFields(fields ...string) *DataSet {
	ds.MasterSource.ClearMasterFields()
	return ds
}

func (ds *DataSet) ToStruct(model any) error {
	modelType := reflect.TypeOf(model)
	modelValue := reflect.ValueOf(model)

	if modelType.Kind() == reflect.Pointer {
		modelType = modelValue.Type().Elem()
		modelValue = modelValue.Elem()
	} else {
		return fmt.Errorf("the variable " + modelType.Name() + " is not a pointer.")
	}

	switch modelType.Kind() {
	case reflect.Struct:
		return ds.toStructUniqResult(modelValue)
	case reflect.Slice, reflect.Array:
		return ds.toStructList(modelValue)
	default:
		return errors.New("The interface is not a slice, array or struct")
	}
}

func (ds *DataSet) toStructUniqResult(modelValue reflect.Value) error {
	for i := 0; i < modelValue.NumField(); i++ {
		if modelValue.Field(i).Kind() != reflect.Slice && modelValue.Field(i).Kind() != reflect.Array {
			field := modelValue.Type().Field(i)
			fieldName := field.Tag.Get("column")

			if fieldName == "" {
				fieldName = field.Name
			}

			if field.Anonymous {
				continue
			}

			var fieldValue any

			if field.Type.Kind() == reflect.Pointer {
				if ds.FieldByName(fieldName).IsNotNull() {
					fieldValue = ds.GetValue(ds.FieldByName(fieldName), modelValue.Field(i).Interface())

					if fieldValue != nil {
						rf := modelValue.Field(i)
						rf = reflect.NewAt(rf.Type(), unsafe.Pointer(rf.UnsafeAddr())).Elem()
						rf.Set(reflect.ValueOf(fieldValue))
					}
				}
			} else {
				fieldValue = ds.GetValue(ds.FieldByName(fieldName), modelValue.Field(i).Interface())

				if fieldValue != nil {
					rf := modelValue.Field(i)
					rf = reflect.NewAt(rf.Type(), unsafe.Pointer(rf.UnsafeAddr())).Elem()
					rf.Set(reflect.ValueOf(fieldValue))
				}
			}
		}
	}

	return nil
}

func (ds *DataSet) GetValue(field *Field, fieldType any) any {
	var fieldValue any
	valueType := reflect.TypeOf(fieldType)
	switch fieldType.(type) {
	case int:
		val := field.AsInt()
		fieldValue = reflect.ValueOf(val).Convert(valueType).Interface()
	case int8:
		val := field.AsInt8()
		fieldValue = reflect.ValueOf(val).Convert(valueType).Interface()
	case int16:
		val := field.AsInt16()
		fieldValue = reflect.ValueOf(val).Convert(valueType).Interface()
	case int32:
		val := field.AsInt32()
		fieldValue = reflect.ValueOf(val).Convert(valueType).Interface()
	case int64:
		val := field.AsInt64()
		fieldValue = reflect.ValueOf(val).Convert(valueType).Interface()
	case float32, float64:
		val := field.AsFloat64()
		fieldValue = reflect.ValueOf(val).Convert(valueType).Interface()
	case string:
		val := field.AsString()
		fieldValue = reflect.ValueOf(val).Convert(valueType).Interface()
	case *int:
		val := field.AsInt()
		fieldValue = reflect.ValueOf(&val).Convert(valueType).Interface()
	case *int8:
		val := field.AsInt8()
		fieldValue = reflect.ValueOf(&val).Convert(valueType).Interface()
	case *int16:
		val := field.AsInt16()
		fieldValue = reflect.ValueOf(&val).Convert(valueType).Interface()
	case *int32:
		val := field.AsInt32()
		fieldValue = reflect.ValueOf(&val).Convert(valueType).Interface()
	case *int64:
		val := field.AsInt64()
		fieldValue = reflect.ValueOf(&val).Convert(valueType).Interface()
	case *float32, *float64:
		val := field.AsFloat64()
		fieldValue = reflect.ValueOf(&val).Convert(valueType).Interface()
	case *string:
		val := field.AsString()
		fieldValue = reflect.ValueOf(&val).Convert(valueType).Interface()
	default:
		fieldValue = field.AsValue()
	}
	return fieldValue
}

func (ds *DataSet) toStructList(modelValue reflect.Value) error {
	var modelType reflect.Type

	if modelValue.Type().Elem().Kind() == reflect.Pointer {
		modelType = modelValue.Type().Elem()
	} else {
		modelType = modelValue.Type().Elem()
	}

	for !ds.Eof() {
		newModel := reflect.New(modelType)

		if modelValue.Type().Elem().Kind() == reflect.Pointer {
			err := ds.toStructUniqResult(reflect.ValueOf(newModel.Interface()).Elem())
			if err != nil {
				return err
			}
		} else {
			err := ds.toStructUniqResult(reflect.ValueOf(newModel.Interface()).Elem())
			if err != nil {
				return err
			}
		}

		err := ds.toStructUniqResult(reflect.ValueOf(newModel.Interface()).Elem())
		if err != nil {
			return err
		}

		modelValue.Set(reflect.Append(modelValue, newModel.Elem()))

		ds.Next()
	}

	return nil
}

func JoinSlice(list any) string {
	valueOf := reflect.ValueOf(list)
	value := make([]string, valueOf.Len())
	for i := 0; i < valueOf.Len(); i++ {
		if valueOf.Index(i).Type().Kind() == reflect.String {
			value[i] = "'" + fmt.Sprintf("%v", valueOf.Index(i).Interface()) + "'"
		} else {
			value[i] = fmt.Sprintf("%v", valueOf.Index(i).Interface())
		}
	}
	return strings.Join(value, ", ")
}

func (ds *DataSet) PrintParam() {
	ds.Params.PrintParam()
}

func generateString() string {
	var result string
	for i := 0; i < 400; i++ {
		result += "abcdefghij"
	}
	return result
}

func (ds *DataSet) ParseSql() (sqlparser.Statement, error) {
	return sqlparser.Parse(ds.GetSql())
}

func limitStr(value string, limit int) string {
	if len(value) > limit {
		return value[:limit]
	}
	return value
}

func (ds *DataSet) SqlParam() string {
	sql := ds.Sql.Text()
	for i := 0; i < len(ds.Params.List); i++ {
		key := ds.Params.List[i].Name
		value := ds.Params.List[i].Value
		switch val := value.Value.(type) {
		case nil:
			sql = strings.ReplaceAll(sql, ":"+key, "null")
		case time.Time:
			data := limitStr(value.AsString(), 19)
			sql = strings.ReplaceAll(sql, ":"+key, "to_date('"+data+"','rrrr-mm-dd hh24:mi:ss')")
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			sql = strings.ReplaceAll(sql, ":"+key, fmt.Sprintf("%v", val))
		case float32, float64:
			sql = strings.ReplaceAll(sql, ":"+key, fmt.Sprintf("%f", val))
		case string:
			sql = strings.ReplaceAll(sql, ":"+key, "'"+value.AsString()+"'")
		}
	}
	return sql
}
