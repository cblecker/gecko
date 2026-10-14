package authz

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"

	"github.com/cedar-policy/cedar-go"

	"k8s.io/apimachinery/pkg/runtime"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CedarContextForObject builds the request context used to evaluate a public
// API object. Only the resource identity and spec are exposed to conditions.
func CedarContextForObject(obj client.Object, resourcePlural, method string) (cedar.Record, error) {
	if obj == nil {
		return cedar.Record{}, fmt.Errorf("object is required")
	}

	objectMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return cedar.Record{}, fmt.Errorf("convert object to unstructured: %w", err)
	}

	var spec any
	if value, found := objectMap["spec"]; found {
		spec = value
	}
	return cedarContext(obj.GetName(), resourcePlural, method, spec)
}

func cedarContext(resourceName, resourcePlural, method string, spec any) (cedar.Record, error) {
	values := cedar.RecordMap{
		"resourcePlural": cedar.String(resourcePlural),
		"method":         cedar.String(method),
	}
	if resourceName != "" {
		values["resourceName"] = cedar.String(resourceName)
	}
	if spec != nil {
		value, err := cedarValue(spec)
		if err != nil {
			return cedar.Record{}, fmt.Errorf("convert spec to Cedar value: %w", err)
		}
		if _, ok := value.(cedar.Record); !ok {
			return cedar.Record{}, fmt.Errorf("spec must be an object, got %T", spec)
		}
		values["spec"] = value
	}
	return cedar.NewRecord(values), nil
}

func cedarValue(value any) (cedar.Value, error) {
	switch typed := value.(type) {
	case string:
		return cedar.String(typed), nil
	case bool:
		return cedar.Boolean(typed), nil
	case int:
		return cedar.Long(typed), nil
	case int8:
		return cedar.Long(typed), nil
	case int16:
		return cedar.Long(typed), nil
	case int32:
		return cedar.Long(typed), nil
	case int64:
		return cedar.Long(typed), nil
	case uint:
		return cedarUnsigned(typed)
	case uint8:
		return cedarUnsigned(typed)
	case uint16:
		return cedarUnsigned(typed)
	case uint32:
		return cedarUnsigned(typed)
	case uint64:
		return cedarUnsigned(typed)
	case float32:
		return cedarFloat(float64(typed))
	case float64:
		return cedarFloat(typed)
	case json.Number:
		return cedarJSONNumber(typed)
	case map[string]any:
		return cedarRecord(typed)
	case []any:
		return cedarSet(typed)
	case nil:
		return nil, fmt.Errorf("null values are not supported")
	default:
		return normalizeCedarValue(typed)
	}
}

func cedarUnsigned[T ~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64](value T) (cedar.Value, error) {
	if uint64(value) > math.MaxInt64 {
		return nil, fmt.Errorf("unsigned integer %d exceeds Cedar Long range", value)
	}
	return cedar.Long(int64(value)), nil
}

func cedarFloat(value float64) (cedar.Value, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil, fmt.Errorf("non-finite number")
	}
	if math.Trunc(value) == value && value >= math.MinInt64 && value <= math.MaxInt64 {
		return cedar.Long(int64(value)), nil
	}
	decimal, err := cedar.NewDecimalFromFloat(value)
	if err != nil {
		return nil, err
	}
	return decimal, nil
}

func cedarJSONNumber(value json.Number) (cedar.Value, error) {
	if integer, err := value.Int64(); err == nil {
		return cedar.Long(integer), nil
	}
	floatValue, err := strconv.ParseFloat(string(value), 64)
	if err != nil {
		return nil, fmt.Errorf("parse number %q: %w", value, err)
	}
	return cedarFloat(floatValue)
}

func cedarRecord(values map[string]any) (cedar.Value, error) {
	record := make(cedar.RecordMap, len(values))
	for key, value := range values {
		if value == nil {
			continue
		}
		converted, err := cedarValue(value)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", key, err)
		}
		record[cedar.String(key)] = converted
	}
	return cedar.NewRecord(record), nil
}

func cedarSet(values []any) (cedar.Value, error) {
	set := make([]cedar.Value, 0, len(values))
	for index, value := range values {
		if value == nil {
			return nil, fmt.Errorf("element %d is null", index)
		}
		converted, err := cedarValue(value)
		if err != nil {
			return nil, fmt.Errorf("element %d: %w", index, err)
		}
		set = append(set, converted)
	}
	return cedar.NewSet(set...), nil
}

func normalizeCedarValue(value any) (cedar.Value, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal %T: %w", value, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return nil, fmt.Errorf("unmarshal %T: %w", value, err)
	}
	return cedarValue(normalized)
}
