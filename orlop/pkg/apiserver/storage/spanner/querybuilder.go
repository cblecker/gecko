package spanner

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/openshift-online/gecko/orlop/pkg/apiserver/storage"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
)

var validLabelKeyRe = regexp.MustCompile(`^[a-zA-Z0-9._\-/]+$`)
var validFieldPathRe = regexp.MustCompile(`^[a-zA-Z0-9._\-/]+$`)

type queryBuilder struct {
	tableName string
	columns   []string
	where     []string
	params    map[string]any
	orderBy   []string
	limit     int64
	paramSeq  int
}

func newQueryBuilder(tableName string, columns ...string) *queryBuilder {
	if len(columns) == 0 {
		columns = []string{"*"}
	}
	return &queryBuilder{
		tableName: tableName,
		columns:   columns,
		params:    map[string]any{},
	}
}

func (qb *queryBuilder) nextParam(value any) string {
	name := fmt.Sprintf("p%d", qb.paramSeq)
	qb.paramSeq++
	qb.params[name] = value
	return "@" + name
}

func (qb *queryBuilder) appendWhere(condition string) {
	qb.where = append(qb.where, condition)
}

func (qb *queryBuilder) whereResourceType(rt string) *queryBuilder {
	p := qb.nextParam(rt)
	qb.appendWhere(fmt.Sprintf("resource_type = %s", p))
	return qb
}

func (qb *queryBuilder) whereContextFilter(value string) *queryBuilder {
	p := qb.nextParam(value)
	qb.appendWhere(fmt.Sprintf("context_filter = %s", p))
	return qb
}

func (qb *queryBuilder) whereNamespace(namespace string) *queryBuilder {
	if namespace == "" {
		return qb
	}
	p := qb.nextParam(namespace)
	qb.appendWhere(fmt.Sprintf("namespace = %s", p))
	return qb
}

func (qb *queryBuilder) whereLabelSelector(selector labels.Selector) *queryBuilder {
	if selector == nil {
		return qb
	}
	requirements, _ := selector.Requirements()
	for _, req := range requirements {
		qb.addLabelRequirement(req)
	}
	return qb
}

func (qb *queryBuilder) addLabelRequirement(req labels.Requirement) {
	key := req.Key()
	if !validLabelKeyRe.MatchString(key) {
		return
	}

	jsonPath := fmt.Sprintf(`JSON_VALUE(labels, '$["%s"]')`, key)
	values := req.Values()

	switch req.Operator() {
	case selection.Exists:
		qb.appendWhere(fmt.Sprintf("%s IS NOT NULL", jsonPath))

	case selection.DoesNotExist:
		qb.appendWhere(fmt.Sprintf("%s IS NULL", jsonPath))

	case selection.Equals, selection.DoubleEquals, selection.In:
		if values.Len() == 1 {
			p := qb.nextParam(values.List()[0])
			qb.appendWhere(fmt.Sprintf("%s = %s", jsonPath, p))
		} else {
			p := qb.nextParam(values.List())
			qb.appendWhere(fmt.Sprintf("%s IN UNNEST(%s)", jsonPath, p))
		}

	case selection.NotEquals, selection.NotIn:
		if values.Len() == 1 {
			p := qb.nextParam(values.List()[0])
			qb.appendWhere(fmt.Sprintf("(%s IS NULL OR %s != %s)", jsonPath, jsonPath, p))
		} else {
			p := qb.nextParam(values.List())
			qb.appendWhere(fmt.Sprintf("(%s IS NULL OR %s NOT IN UNNEST(%s))", jsonPath, jsonPath, p))
		}
	}
}

func (qb *queryBuilder) whereShardSelector(selector *storage.ShardSelector) *queryBuilder {
	if selector == nil {
		return qb
	}
	hashSQL := buildShardHashSQL()
	pCount := qb.nextParam(int64(selector.Count))
	pIndex := qb.nextParam(int64(selector.Index))
	qb.appendWhere(fmt.Sprintf("MOD(MOD(%s, %s) + %s, %s) = %s", hashSQL, pCount, pCount, pCount, pIndex))
	return qb
}

func buildShardHashSQL() string {
	hashExpr := "TO_CODE_POINTS(SHA256(CAST(CONCAT(namespace, '/', name) AS BYTES)))"
	var parts []string
	for i := range 8 {
		shift := 56 - (i * 8)
		parts = append(parts, fmt.Sprintf("CAST(h[OFFSET(%d)] AS INT64) << %d", i, shift))
	}
	return fmt.Sprintf("(SELECT %s FROM UNNEST([%s]) AS h)", strings.Join(parts, " | "), hashExpr)
}

func (qb *queryBuilder) whereFieldFilters(filters map[string]string) *queryBuilder {
	for path, value := range filters {
		if !validFieldPathRe.MatchString(path) {
			continue
		}
		p := qb.nextParam(value)
		qb.appendWhere(fmt.Sprintf("JSON_VALUE(data, '$.%s') = %s", path, p))
	}
	return qb
}

func (qb *queryBuilder) whereContinueToken(token *storage.ContinueToken) *queryBuilder {
	if token == nil {
		return qb
	}
	if token.Namespace != "" {
		pNs := qb.nextParam(token.Namespace)
		pName := qb.nextParam(token.Name)
		qb.appendWhere(fmt.Sprintf("(namespace > %s OR (namespace = %s AND name > %s))", pNs, pNs, pName))
	} else {
		pName := qb.nextParam(token.Name)
		qb.appendWhere(fmt.Sprintf("name > %s", pName))
	}
	return qb
}

func (qb *queryBuilder) setOrderBy(columns ...string) *queryBuilder {
	qb.orderBy = append(qb.orderBy, columns...)
	return qb
}

func (qb *queryBuilder) setLimit(limit int64) *queryBuilder {
	if limit > 0 {
		qb.limit = limit
	}
	return qb
}

func (qb *queryBuilder) build() (string, map[string]any) {
	query := fmt.Sprintf("SELECT %s FROM %s", strings.Join(qb.columns, ", "), qb.tableName)

	if len(qb.where) > 0 {
		query += " WHERE " + strings.Join(qb.where, " AND ")
	}

	if len(qb.orderBy) > 0 {
		query += " ORDER BY " + strings.Join(qb.orderBy, ", ")
	}

	if qb.limit > 0 {
		p := qb.nextParam(qb.limit)
		query += fmt.Sprintf(" LIMIT %s", p)
	}

	return query, qb.params
}
