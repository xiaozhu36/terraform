package oss

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/aliyun/aliyun-oss-go-sdk/oss"
	"github.com/hashicorp/terraform/backend"
	"github.com/hashicorp/terraform/state"
	"github.com/hashicorp/terraform/state/remote"
	"github.com/hashicorp/terraform/terraform"
	"log"
	"path"
)

const (
	lockFileSuffix = ".tflock"
)

// get a remote client configured for this state
func (b *Backend) remoteClient(name string) (*RemoteClient, error) {
	if name == "" {
		return nil, errors.New("missing state name")
	}

	client := &RemoteClient{
		ossClient:            b.ossClient,
		bucketName:           b.bucketName,
		statePath:            b.statePath(name),
		lockPath:             b.lockPath(name),
		serverSideEncryption: b.serverSideEncryption,
		acl:                  b.acl,
	}

	return client, nil
}

func (b *Backend) State(name string) (state.State, error) {
	client, err := b.remoteClient(name)
	if err != nil {
		return nil, err
	}

	stateMgr := &remote.State{Client: client}

	// Check to see if this state already exists.
	existing, err := b.States()
	if err != nil {
		return nil, err
	}
	log.Printf("Current state name: %s. All States:%#v", name, existing)

	exists := false
	for _, s := range existing {
		if s == name {
			exists = true
			break
		}
	}
	// We need to create the object so it's listed by States.
	if !exists {
		// take a lock on this state while we write it
		lockInfo := state.NewLockInfo()
		lockInfo.Operation = "init"
		lockId, err := client.Lock(lockInfo)
		if err != nil {
			return nil, fmt.Errorf("Failed to lock OSS state: %s", err)
		}

		// Local helper function so we can call it multiple places
		lockUnlock := func(e error) error {
			if err := stateMgr.Unlock(lockId); err != nil {
				return fmt.Errorf(strings.TrimSpace(stateUnlockError), lockId, err)
			}
			return e
		}

		// Grab the value
		// This is to ensure that no one beat us to writing a state between
		// the `exists` check and taking the lock.
		if err := stateMgr.RefreshState(); err != nil {
			err = lockUnlock(err)
			return nil, err
		}

		// If we have no state, we have to create an empty state
		if v := stateMgr.State(); v == nil {
			if err := stateMgr.WriteState(terraform.NewState()); err != nil {
				err = lockUnlock(err)
				return nil, err
			}
			if err := stateMgr.PersistState(); err != nil {
				err = lockUnlock(err)
				return nil, err
			}
		}

		// Unlock, the state should now be initialized
		if err := lockUnlock(nil); err != nil {
			return nil, err
		}

	}
	return stateMgr, nil
}

func (b *Backend) States() ([]string, error) {
	bucket, err := b.ossClient.Bucket(b.bucketName)
	if err != nil {
		return []string{""}, fmt.Errorf("Error getting bucket: %#v", err)
	}

	var options []oss.Option
	options = append(options, oss.Prefix(b.workspaceKeyPrefix))
	resp, err := bucket.ListObjects(options...)

	if err != nil {
		return nil, err
	}

	workspaces := []string{backend.DefaultStateName}
	for _, obj := range resp.Objects {
		workspace := b.keyEnv(obj.Key)
		if workspace != "" {
			workspaces = append(workspaces, workspace)
		}
	}

	sort.Strings(workspaces[1:])
	return workspaces, nil
}

func (b *Backend) DeleteState(name string) error {
	if name == backend.DefaultStateName || name == "" {
		return fmt.Errorf("can't delete default state")
	}

	client, err := b.remoteClient(name)
	if err != nil {
		return err
	}
	log.Printf("Delete state %s ...", name)
	return client.Delete()
}

// extract the object name from the OSS key
func (b *Backend) keyEnv(key string) string {
	// we have 3 parts, the workspace key prefix, the workspace name, and the state key name
	parts := strings.SplitN(key, "/", 3)
	if len(parts) < 3 {
		// no workspace prefix here
		return ""
	}

	// shouldn't happen since we listed by prefix
	if parts[0] != b.workspaceKeyPrefix {
		return ""
	}

	// not our key, so don't include it in our listing
	if parts[2] != b.keyName {
		return ""
	}

	return parts[1]
}

func (b *Backend) statePath(name string) string {
	if name == backend.DefaultStateName && b.keyName != "" {
		return b.keyName
	}
	return path.Join(b.workspaceKeyPrefix, name, b.keyName)
}

func (b *Backend) lockPath(name string) string {
	if name == backend.DefaultStateName && b.keyName != "" {
		return b.keyName + lockFileSuffix
	}
	return path.Join(b.workspaceKeyPrefix, name, b.keyName+lockFileSuffix)
}

const stateUnlockError = `
Error unlocking Alicloud OSS state file:

Lock ID: %s
Error message: %#v

You may have to force-unlock this state in order to use it again.
The Alibaba Cloud backend acquires a lock during initialization to ensure the initial state file is created.
`
