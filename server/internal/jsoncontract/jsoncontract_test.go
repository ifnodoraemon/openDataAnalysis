package jsoncontract

import (
	"strings"
	"testing"
)

type decodeTarget struct {
	Name  string `json:"name"`
	Limit int    `json:"limit"`
}

func TestDecodeAcceptsStrictObject(t *testing.T) {
	var out decodeTarget
	if err := Decode([]byte(`{"name":"sales","limit":10}`), &out); err != nil {
		t.Fatalf("expected decode success, got %v", err)
	}
	if out.Name != "sales" || out.Limit != 10 {
		t.Fatalf("unexpected decode result: %#v", out)
	}
}

func TestDecodeRejectsDuplicateKeys(t *testing.T) {
	cases := []string{
		`{"name":"a","name":"b"}`,
		`{"name":"a","limit":1,"nested":{"name":"x","name":"y"}}`,
	}
	for _, raw := range cases {
		var out map[string]interface{}
		if err := Decode([]byte(raw), &out); err == nil {
			t.Fatalf("expected duplicate key rejection for %s", raw)
		} else if !strings.Contains(err.Error(), "duplicate") {
			t.Fatalf("expected duplicate key error for %s, got %v", raw, err)
		}
	}
}

func TestDecodeRejectsTrailingDataAndMultipleValues(t *testing.T) {
	cases := []string{
		`{"name":"a"} {"name":"b"}`,
		`{"name":"a"} garbage`,
		`{"name":"a"} 123`,
	}
	for _, raw := range cases {
		var out decodeTarget
		if err := Decode([]byte(raw), &out); err == nil {
			t.Fatalf("expected rejection for %s", raw)
		}
	}
}

func TestDecodeRejectsUnknownFields(t *testing.T) {
	var out decodeTarget
	if err := Decode([]byte(`{"name":"a","limit":1,"extra":true}`), &out); err == nil {
		t.Fatalf("expected unknown field rejection")
	}
}

func TestValidateRejectsNonStringObjectKeys(t *testing.T) {
	if err := Validate([]byte(`{1:"a"}`)); err == nil {
		t.Fatalf("expected non-string key rejection")
	}
}

func TestDecodeNumbersByDestinationType(t *testing.T) {
	var out decodeTarget
	if err := Decode([]byte(`{"name":"a","limit":1.0}`), &out); err == nil {
		t.Fatalf("expected fractional number rejection for int destination")
	}
	if err := Decode([]byte(`{"name":"a","limit":1}`), &out); err != nil {
		t.Fatalf("expected integer decode success, got %v", err)
	}
	if out.Limit != 1 {
		t.Fatalf("unexpected limit: %d", out.Limit)
	}

	var generic map[string]interface{}
	if err := Decode([]byte(`{"limit":1.0}`), &generic); err != nil {
		t.Fatalf("expected generic decode success, got %v", err)
	}
	if limit, ok := generic["limit"].(float64); !ok || limit != 1 {
		t.Fatalf("expected float64 1 for untyped destination, got %T %#v", generic["limit"], generic["limit"])
	}
}

func TestValidateAcceptsNestedStructures(t *testing.T) {
	raw := `{"a":{"b":[1,2,{"c":null}]},"d":[],"e":true}`
	if err := Validate([]byte(raw)); err != nil {
		t.Fatalf("expected valid nested JSON, got %v", err)
	}
}
