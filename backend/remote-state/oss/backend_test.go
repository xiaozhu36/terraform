package oss

import (
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/aliyun/aliyun-oss-go-sdk/oss"
	"github.com/hashicorp/terraform/backend"
	"github.com/hashicorp/terraform/config"
	"github.com/hashicorp/terraform/state/remote"
	"github.com/hashicorp/terraform/terraform"
	"strings"
)

// verify that we are doing ACC tests or the OSS tests specifically
func testACC(t *testing.T) {
	skip := os.Getenv("TF_ACC") == "" && os.Getenv("TF_OSS_TEST") == ""
	if skip {
		t.Log("oss backend tests require setting TF_ACC or TF_OSS_TEST")
		t.Skip()
	}
	if os.Getenv("ALICLOUD_REGION") == "" {
		os.Setenv("ALICLOUD_REGION", "cn-beijing")
	}
}

func TestBackend_impl(t *testing.T) {
	var _ backend.Backend = new(Backend)
}

func TestBackendConfig(t *testing.T) {
	testACC(t)
	config := map[string]interface{}{
		"region": "cn-beijing",
		"bucket": "terraform-backend-oss-test",
		"key":    "mystate",
	}

	b := backend.TestBackendConfig(t, New(), config).(*Backend)

	if !strings.HasPrefix(b.ossClient.Config.Endpoint, "http://oss-cn-beijing") {
		t.Fatalf("Incorrect region was provided")
	}
	if b.bucketName != "terraform-backend-oss-test" {
		t.Fatalf("Incorrect bucketName was provided")
	}
	if b.keyName != "mystate" {
		t.Fatalf("Incorrect keyName was provided")
	}

	if b.ossClient.Config.AccessKeyID == "" {
		t.Fatalf("No Access Key Id was provided")
	}
	if b.ossClient.Config.AccessKeySecret == "" {
		t.Fatalf("No Secret Access Key was provided")
	}
}

func TestBackendConfig_invalidKey(t *testing.T) {
	testACC(t)
	cfg := map[string]interface{}{
		"region":  "cn-beijing",
		"bucket":  "terraform-backend-oss-test",
		"key":     "/leading-slash",
		"encrypt": true,
	}

	rawCfg, err := config.NewRawConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	resCfg := terraform.NewResourceConfig(rawCfg)

	_, errs := New().Validate(resCfg)
	if len(errs) != 1 {
		t.Fatal("expected config validation error")
	}
}

func TestBackend(t *testing.T) {
	testACC(t)

	bucketName := fmt.Sprintf("terraform-remote-oss-test-%x", time.Now().Unix())
	keyName := "testTFState"

	b := backend.TestBackendConfig(t, New(), map[string]interface{}{
		"bucket":  bucketName,
		"key":     keyName,
		"encrypt": true,
	}).(*Backend)

	createOSSBucket(t, b.ossClient, bucketName)
	defer deleteOSSBucket(t, b.ossClient, bucketName)

	backend.TestBackend(t, b, nil)
}

func TestBackendLocked(t *testing.T) {
	testACC(t)

	bucketName := fmt.Sprintf("terraform-remote-oss-test-%x", time.Now().Unix())
	keyName := "tfTest/mystate"

	b1 := backend.TestBackendConfig(t, New(), map[string]interface{}{
		"bucket":  bucketName,
		"key":     keyName,
		"encrypt": true,
	}).(*Backend)

	b2 := backend.TestBackendConfig(t, New(), map[string]interface{}{
		"bucket":  bucketName,
		"key":     keyName,
		"encrypt": true,
	}).(*Backend)

	createOSSBucket(t, b1.ossClient, bucketName)
	defer deleteOSSBucket(t, b1.ossClient, bucketName)

	backend.TestBackend(t, b1, b2)
}

// add some extra junk in OSS to try and confuse the env listing.
func TestBackendExtraPaths(t *testing.T) {
	testACC(t)
	bucketName := fmt.Sprintf("terraform-remote-oss-test-%x", time.Now().Unix())
	keyName := "test/state/tfstate"

	b := backend.TestBackendConfig(t, New(), map[string]interface{}{
		"bucket":  bucketName,
		"key":     keyName,
		"encrypt": true,
	}).(*Backend)

	createOSSBucket(t, b.ossClient, bucketName)
	defer deleteOSSBucket(t, b.ossClient, bucketName)

	// put multiple states in old env paths.
	foo := terraform.NewState()
	bar := terraform.NewState()

	// RemoteClient to Put things in various paths
	client := &RemoteClient{
		ossClient:            b.ossClient,
		bucketName:           b.bucketName,
		statePath:            b.statePath("foo"),
		lockPath:             b.lockPath("foo"),
		serverSideEncryption: b.serverSideEncryption,
		acl:                  b.acl,
	}

	stateMgr := &remote.State{Client: client}
	stateMgr.WriteState(foo)
	if err := stateMgr.PersistState(); err != nil {
		t.Fatal(err)
	}

	client.statePath = b.statePath("bar")
	stateMgr.WriteState(bar)
	if err := stateMgr.PersistState(); err != nil {
		t.Fatal(err)
	}

	if err := checkStateList(b, []string{"default", "bar", "foo"}); err != nil {
		t.Fatal(err)
	}

	// put a state in an env directory name
	client.statePath = b.workspaceKeyPrefix + "/error"
	stateMgr.WriteState(terraform.NewState())
	if err := stateMgr.PersistState(); err != nil {
		t.Fatal(err)
	}
	if err := checkStateList(b, []string{"default", "bar", "foo"}); err != nil {
		t.Fatal(err)
	}

	// add state with the wrong key for an existing env
	client.statePath = b.workspaceKeyPrefix + "/bar/notTestState"
	stateMgr.WriteState(terraform.NewState())
	if err := stateMgr.PersistState(); err != nil {
		t.Fatal(err)
	}
	if err := checkStateList(b, []string{"default", "bar", "foo"}); err != nil {
		t.Fatal(err)
	}

	// remove the state with extra subkey
	if err := b.DeleteState("bar"); err != nil {
		t.Fatal(err)
	}

	if err := checkStateList(b, []string{"default", "foo"}); err != nil {
		t.Fatal(err)
	}

	// fetch that state again, which should produce a new lineage
	barMgr, err := b.State("bar")
	if err != nil {
		t.Fatal(err)
	}
	if err := barMgr.RefreshState(); err != nil {
		t.Fatal(err)
	}

	if barMgr.State().Lineage == bar.Lineage {
		t.Fatal("state bar was not deleted")
	}
	bar = barMgr.State()

	// add a state with a key that matches an existing environment dir name
	client.statePath = b.workspaceKeyPrefix + "/bar/"
	stateMgr.WriteState(terraform.NewState())
	if err := stateMgr.PersistState(); err != nil {
		t.Fatal(err)
	}

	// make sure s2 is OK
	barMgr, err = b.State("bar")
	if err != nil {
		t.Fatal(err)
	}
	if err := barMgr.RefreshState(); err != nil {
		t.Fatal(err)
	}

	if barMgr.State().Lineage != bar.Lineage {
		t.Fatal("we got the wrong state for bar")
	}

	if err := checkStateList(b, []string{"default", "bar", "foo"}); err != nil {
		t.Fatal(err)
	}
}

func checkStateList(b backend.Backend, expected []string) error {
	states, err := b.States()
	if err != nil {
		return err
	}

	if !reflect.DeepEqual(states, expected) {
		return fmt.Errorf("incorrect states listed: %q", states)
	}
	return nil
}

func createOSSBucket(t *testing.T, ossClient *oss.Client, bucketName string) {
	// Be clear about what we're doing in case the user needs to clean
	// this up later.
	t.Logf("creating OSS bucket %s in %s", bucketName, ossClient.Config.Endpoint)
	if err := ossClient.CreateBucket(bucketName); err != nil {
		t.Fatal("failed to create test OSS bucket:", err)
	}
}

func deleteOSSBucket(t *testing.T, ossClient *oss.Client, bucketName string) {
	warning := "WARNING: Failed to delete the test OSS bucket. It may have been left in your Alicloud account and may incur storage charges. (error was %s)"

	// first we have to get rid of the env objects, or we can't delete the bucket
	bucket, err := ossClient.Bucket(bucketName)
	if err != nil {
		t.Fatal("Error getting bucket:", err)
		return
	}
	objects, err := bucket.ListObjects()
	if err != nil {
		t.Logf(warning, err)
		return
	}
	for _, obj := range objects.Objects {
		if err := bucket.DeleteObject(obj.Key); err != nil {
			// this will need cleanup no matter what, so just warn and exit
			t.Logf(warning, err)
			return
		}
	}

	if err := ossClient.DeleteBucket(bucketName); err != nil {
		t.Logf(warning, err)
	}
}
