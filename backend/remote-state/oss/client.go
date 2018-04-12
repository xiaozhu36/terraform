package oss

import (
	"bytes"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"io"

	"github.com/aliyun/aliyun-oss-go-sdk/oss"
	multierror "github.com/hashicorp/go-multierror"
	uuid "github.com/hashicorp/go-uuid"
	"github.com/hashicorp/terraform/state"
	"github.com/hashicorp/terraform/state/remote"
	"log"
)

type RemoteClient struct {
	ossClient            *oss.Client
	bucketName           string
	statePath            string
	lockPath             string
	serverSideEncryption bool
	acl                  string
}

func (c *RemoteClient) Get() (payload *remote.Payload, err error) {
	buf, err := c.getObj(c.statePath)
	if err != nil {
		return nil, err
	}

	// If there was no data, then return nil
	if buf == nil || len(buf.Bytes()) == 0 {
		log.Printf("State %s has no data.", c.statePath)
		return nil, nil
	}

	var hashChannel = make(chan []byte, 1)
	sum := md5.Sum(buf.Bytes())
	hashChannel <- sum[:]
	payload = &remote.Payload{
		Data: buf.Bytes(),
		MD5:  <-hashChannel,
	}
	return payload, nil
}

func (c *RemoteClient) Put(data []byte) error {
	return c.putObj(c.statePath, data)
}

func (c *RemoteClient) Delete() error {
	return c.deleteObj(c.statePath)
}

func (c *RemoteClient) Lock(info *state.LockInfo) (string, error) {
	bucket, err := c.ossClient.Bucket(c.bucketName)
	if err != nil {
		return "", fmt.Errorf("Error getting bucket: %#v", err)
	}

	log.Printf("Lock info:%#v", info)

	infoJson, err := json.Marshal(info)
	if err != nil {
		return "", err
	}

	if info.ID == "" {
		lockID, err := uuid.GenerateUUID()
		if err != nil {
			return "", err
		}
		info.ID = lockID
	}
	info.Path = c.lockPath
	if exist, err := bucket.IsObjectExist(info.Path); err != nil {
		return "", fmt.Errorf("Estimating object %s is exist got an error: %#v", info.Path, err)
	} else if !exist {
		if err := c.putObj(info.Path, infoJson); err != nil {
			return "", err
		}
	} else if _, err := c.validLock(info.ID); err != nil {
		return "", err
	}

	return info.ID, nil
}

func (c *RemoteClient) Unlock(id string) error {
	log.Printf("UnLock info %s", id)
	lockInfo, err := c.validLock(id)
	if err != nil {
		return err
	}

	if err := c.deleteObj(c.lockPath); err != nil {
		return &state.LockError{
			Info: lockInfo,
			Err:  err,
		}
	}
	return nil
}

func (c *RemoteClient) putObj(key string, data []byte) error {
	log.Printf("Put Object %s.", key)
	bucket, err := c.ossClient.Bucket(c.bucketName)
	if err != nil {
		return fmt.Errorf("Error getting bucket: %#v", err)
	}
	body := bytes.NewReader(data)

	var options []oss.Option
	if c.acl != "" {
		options = append(options, oss.ACL(oss.ACLType(c.acl)))
	}
	options = append(options, oss.ContentType("application/json"))
	if c.serverSideEncryption {
		options = append(options, oss.ServerSideEncryption("AES256"))
	}
	options = append(options, oss.ContentLength(int64(len(data))))

	if body != nil {
		if err := bucket.PutObject(key, body, options...); err != nil {
			return fmt.Errorf("failed to upload %s: %#v", key, err)
		}
		log.Printf("Put Object %s successfully.", key)
		return nil
	}
	return nil
}

func (c *RemoteClient) getObj(key string) (*bytes.Buffer, error) {
	log.Printf("Get Object %s.", key)
	bucket, err := c.ossClient.Bucket(c.bucketName)
	if err != nil {
		return nil, fmt.Errorf("Error getting bucket: %#v", err)
	}

	if exist, err := bucket.IsObjectExist(key); err != nil {
		return nil, fmt.Errorf("Estimating object %s is exist got an error: %#v", key, err)
	} else if !exist {
		return nil, nil
	}

	var options []oss.Option
	output, err := bucket.GetObject(key, options...)
	if err != nil {
		return nil, fmt.Errorf("Error getting object: %#v", err)
	}

	//defer output
	buf := bytes.NewBuffer(nil)
	if _, err := io.Copy(buf, output); err != nil {
		return nil, fmt.Errorf("Failed to read remote state: %s", err)
	}
	log.Printf("Get Object %s successfully.", key)
	return buf, nil
}

func (c *RemoteClient) deleteObj(key string) error {
	log.Printf("Delete Object %s.", key)
	bucket, err := c.ossClient.Bucket(c.bucketName)
	if err != nil {
		return fmt.Errorf("Error getting bucket: %#v", err)
	}

	exist, err := bucket.IsObjectExist(key)
	if err != nil {
		return fmt.Errorf("OSS ensure object existing got an error: %#v", err)
	}

	if !exist {
		return nil
	}

	if err := bucket.DeleteObject(key); err != nil {
		return fmt.Errorf("Error deleting object %s: %#v", key, err)
	}
	log.Printf("Delete Object %s successfully.", key)
	return nil
}

func (c *RemoteClient) lockError(err error) *state.LockError {
	lockErr := &state.LockError{
		Err: err,
	}

	info, infoErr := c.lockInfo()
	if infoErr != nil {
		lockErr.Err = multierror.Append(lockErr.Err, infoErr)
	} else {
		lockErr.Info = info
	}
	return lockErr
}

// lockInfo reads the lock file, parses its contents and returns the parsed
// LockInfo struct.
func (c *RemoteClient) lockInfo() (*state.LockInfo, error) {
	buf, err := c.getObj(c.lockPath)
	if err != nil {
		return nil, err
	}
	if buf == nil || len(buf.Bytes()) == 0 {
		return nil, nil
	}
	info := &state.LockInfo{}
	if err := json.Unmarshal(buf.Bytes(), info); err != nil {
		return nil, err
	}

	return info, nil
}

func (c *RemoteClient) validLock(id string) (*state.LockInfo, *state.LockError) {
	lockErr := &state.LockError{}
	lockInfo, err := c.lockInfo()
	if err != nil {
		lockErr.Err = fmt.Errorf("failed to retrieve lock info: %s", err)
		return nil, lockErr
	}
	lockErr.Info = lockInfo

	if lockInfo.ID != id {
		lockErr.Err = fmt.Errorf("lock id %q does not match existing lock", id)
		return nil, lockErr
	}
	return lockInfo, nil
}
