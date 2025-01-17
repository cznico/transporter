package v7

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"transporter/adaptor"
	"transporter/adaptor/elasticsearch/clients"
	"transporter/client"
	"transporter/log"
	"transporter/message"
	"transporter/message/ops"
)

const (
	defaultURL         = "http://127.0.0.1:9200"
	defaultIndex       = "test_v7"
	testType           = "test"
	parentDefaultIndex = "parent_test_v7"
)

var (
	testURL = os.Getenv("ES_V5_URL")
)

func fullURL(suffix string) string {
	return fmt.Sprintf("%s/%s%s", testURL, defaultIndex, suffix)
}

func parentFullURL(suffix string) string {
	return fmt.Sprintf("%s/%s%s", testURL, parentDefaultIndex, suffix)
}

func setup() error {
	log.Debugln("setting up tests")
	return clearTestData()
}

func clearTestData() error {
	req, _ := http.NewRequest(http.MethodDelete, fullURL(""), nil)
	resp, err := http.DefaultClient.Do(req)
	log.Debugf("clearTestData response, %+v", resp)
	parentReq, _ := http.NewRequest(http.MethodDelete, parentFullURL(""), nil)
	parentResp, err := http.DefaultClient.Do(parentReq)
	log.Debugf("clearTestData response, %+v", parentResp)
	return err
}
func createMapping() error {
	// create a simple mapping one company has many employees
	var mapping = []byte(`{"mappings": {"company": {}, "employee": {"_parent": {"type": "company"} } } }`)
	req, _ := http.NewRequest("PUT", parentFullURL(""), bytes.NewBuffer(mapping))
	_, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Debugf("creating Elasticsearch Mapping request failed, %s", err)
	}
	return err
}

func TestMain(m *testing.M) {
	if testURL == "" {
		testURL = defaultURL
	}

	if err := setup(); err != nil {
		log.Errorf("unable to setup tests, %s", err)
		os.Exit(1)
	}
	code := m.Run()
	shutdown()
	os.Exit(code)
}

func shutdown() {
	log.Debugln("shutting down tests")
	clearTestData()
	log.Debugln("tests shutdown complete")
}

type elasticResponse struct {
	Count int `json:"count"`
	Hits  struct {
		Hits []struct {
			ID      string `json:"_id"`
			Parent  string `json:"_parent"`
			Routing string `json:"_routing"`
			Name    string `json:"name"`
			Type    string `json:"_type"`
		} `json:"hits"`
	} `json:"hits"`
}

/**
 * This tests non-parent-child insert,update,delete
 */
func TestWriter(t *testing.T) {
	confirms, cleanup := adaptor.MockConfirmWrites()
	defer adaptor.VerifyWriteConfirmed(cleanup, t)
	opts := &clients.ClientOptions{
		URLs:       []string{testURL},
		HTTPClient: http.DefaultClient,
		Index:      defaultIndex,
	}
	vc := clients.Clients["v7"]
	w, _ := vc.Creator(opts)
	w.Write(
		message.WithConfirms(
			confirms,
			message.From(ops.Insert, testType, map[string]interface{}{"hello": "world"})),
	)(nil)
	w.Write(
		message.WithConfirms(
			confirms,
			message.From(ops.Insert, testType, map[string]interface{}{"_id": "booya", "hello": "world"})),
	)(nil)
	w.Write(
		message.WithConfirms(
			confirms,
			message.From(ops.Update, testType, map[string]interface{}{"_id": "booya", "hello": "goodbye"})),
	)(nil)
	w.Write(
		message.WithConfirms(
			confirms,
			message.From(ops.Delete, testType, map[string]interface{}{"_id": "booya", "hello": "goodbye"})),
	)(nil)
	w.(client.Closer).Close()

	if _, err := http.Get(fullURL("/_refresh")); err != nil {
		t.Fatalf("_refresh request failed, %s", err)
	}
	time.Sleep(1 * time.Second)

	resp, err := http.Get(fullURL("/_count"))
	if err != nil {
		t.Fatalf("_count request failed, %s", err)
	}
	defer resp.Body.Close()
	var r elasticResponse
	json.NewDecoder(resp.Body).Decode(&r)
	if r.Count != 1 {
		t.Errorf("mismatched doc count, expected 1, got %d", r.Count)
	}
}

/**
 * This tests parent-child inserts and updates
 */
func TestWithParentWriter(t *testing.T) {
	confirms, cleanup := adaptor.MockConfirmWrites()
	defer adaptor.VerifyWriteConfirmed(cleanup, t)
	opts := &clients.ClientOptions{
		URLs:       []string{testURL},
		HTTPClient: http.DefaultClient,
		Index:      parentDefaultIndex,
		ParentID:   "parent_id",
	}
	// create mapping
	createMapping()
	vc := clients.Clients["v7"]
	w, _ := vc.Creator(opts)
	// insert parent
	w.Write(
		message.WithConfirms(
			confirms,
			message.From(ops.Insert, "company", map[string]interface{}{"_id": "9g2g", "name": "gingerbreadhouse"})),
	)(nil)
	// insert child
	w.Write(
		message.WithConfirms(
			confirms,
			message.From(ops.Insert, "employee", map[string]interface{}{"_id": "9g6g", "name": "witch", "parent_id": "gingerbreadhouse"})),
	)(nil)
	// update child
	w.Write(
		message.WithConfirms(
			confirms,
			message.From(ops.Update, "employee", map[string]interface{}{"_id": "9g6g", "name": "wickedwitch", "parent_id": "gingerbreadhouse"})),
	)(nil)
	w.(client.Closer).Close()
	if _, err := http.Get(parentFullURL("/_refresh")); err != nil {
		t.Fatalf("_refresh request failed, %s", err)
	}
	time.Sleep(1 * time.Second)
	countResp, err := http.Get(parentFullURL("/_count"))
	if err != nil {
		t.Fatalf("_count request failed, %s", err)
	}
	defer countResp.Body.Close()
	var r elasticResponse
	json.NewDecoder(countResp.Body).Decode(&r)

	// both parent and child should've gotten inserted correctly
	if r.Count != 2 {
		t.Errorf("mismatched doc count, expected 2, got %d", r.Count)
	}
	employeeResp, err := http.Get(parentFullURL("/employee/_search"))
	if err != nil {
		t.Fatalf("_count request failed, %s", err)
	}
	defer employeeResp.Body.Close()

	var par elasticResponse
	// decode and make sure that _parent is in the json response
	json.NewDecoder(employeeResp.Body).Decode(&par)
	if par.Hits.Hits[0].Parent != "gingerbreadhouse" {
		t.Errorf("mismatched _parent, got %d", par.Hits.Hits[0].Parent)
	}
	// decode and make sure that _parent and _routing is in the json response
	if par.Hits.Hits[0].Routing != par.Hits.Hits[0].Parent {
		t.Errorf("mismatched _routing does not equal _parent, got %d", par.Hits.Hits[0].Parent)
	}
	// decode and make sure that _parent and _routing is in the json response
	if par.Hits.Hits[0].Name == "wickedwitch" {
		t.Errorf("mismatched _routing does not equal _parent, got %d", par.Hits.Hits[0].Parent)
	}

	w2, _ := vc.Creator(opts)
	// delete child
	w2.Write(
		message.WithConfirms(
			confirms,
			message.From(ops.Delete, "employee", map[string]interface{}{"_id": "9g6g", "name": "wickedwitch", "parent_id": "gingerbreadhouse"})),
	)(nil)
	w2.(client.Closer).Close()
	time.Sleep(1 * time.Second)
	deletedCountResp, err := http.Get(parentFullURL("/employee/_count"))
	if err != nil {
		t.Fatalf("_count request failed, %s", err)
	}
	defer deletedCountResp.Body.Close()
	// make sure count is 1
	var dr elasticResponse
	json.NewDecoder(deletedCountResp.Body).Decode(&dr)
	if dr.Count != 0 {
		t.Errorf("mismatched doc count, expected 0, got %d", dr.Count)
	}
}