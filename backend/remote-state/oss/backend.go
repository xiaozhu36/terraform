package oss

import (
	"context"
	"fmt"
	"github.com/aliyun/aliyun-oss-go-sdk/oss"
	"github.com/denverdino/aliyungo/common"
	"github.com/denverdino/aliyungo/location"
	"github.com/hashicorp/terraform/backend"
	"github.com/hashicorp/terraform/helper/schema"
	"github.com/hashicorp/terraform/terraform"
	"os"
	"strings"

	"log"
)

// New creates a new backend for OSS remote state.
func New() backend.Backend {
	s := &schema.Backend{
		Schema: map[string]*schema.Schema{
			"access_key": &schema.Schema{
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Alibaba Cloud Access Key ID",
				DefaultFunc: schema.EnvDefaultFunc("ALICLOUD_ACCESS_KEY", os.Getenv("ALICLOUD_ACCESS_KEY_ID")),
			},

			"secret_key": &schema.Schema{
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Alibaba Cloud Access Secret Key",
				DefaultFunc: schema.EnvDefaultFunc("ALICLOUD_SECRET_KEY", os.Getenv("ALICLOUD_ACCESS_KEY_SECRET")),
			},

			"security_token": &schema.Schema{
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Alibaba Cloud Security Token",
				DefaultFunc: schema.EnvDefaultFunc("ALICLOUD_SECURITY_TOKEN", os.Getenv("SECURITY_TOKEN")),
			},

			"region": &schema.Schema{
				Type:        schema.TypeString,
				Optional:    true,
				Description: "The region of the OSS bucket.",
				DefaultFunc: schema.EnvDefaultFunc("ALICLOUD_REGION", os.Getenv("ALICLOUD_DEFAULT_REGION")),
			},

			"bucket": &schema.Schema{
				Type:        schema.TypeString,
				Required:    true,
				Description: "The name of the OSS bucket",
			},

			"path": &schema.Schema{
				Type:        schema.TypeString,
				Required:    true,
				Description: "The path relative to your object storage directory where the state file will be stored.",
			},

			"name": &schema.Schema{
				Type:        schema.TypeString,
				Optional:    true,
				Description: "The name of the state file inside the bucket",
				ValidateFunc: func(v interface{}, s string) ([]string, []error) {
					if strings.HasPrefix(v.(string), "/") || strings.HasSuffix(v.(string), "/") {
						return nil, []error{fmt.Errorf("name can not start and end with '/'")}
					}
					return nil, nil
				},
				Default: "terraform.tfstate",
			},

			"lock": &schema.Schema{
				Type:        schema.TypeBool,
				Optional:    true,
				Description: "Whether to lock state access. Defaults to true",
				Default:     true,
			},

			"encrypt": &schema.Schema{
				Type:        schema.TypeBool,
				Optional:    true,
				Description: "Whether to enable server side encryption of the state file",
				Default:     false,
			},

			"acl": &schema.Schema{
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Object ACL to be applied to the state file",
				Default:     "",
				ValidateFunc: func(v interface{}, k string) ([]string, []error) {
					if value := v.(string); value != "" {
						acls := oss.ACLType(value)
						if acls != oss.ACLPrivate && acls != oss.ACLPublicRead && acls != oss.ACLPublicReadWrite {
							return nil, []error{fmt.Errorf(
								"%q must be a valid ACL value , expected %s, %s or %s, got %q",
								k, oss.ACLPrivate, oss.ACLPublicRead, oss.ACLPublicReadWrite, acls)}
						}
					}
					return nil, nil
				},
			},
		},
	}

	result := &Backend{Backend: s}
	result.Backend.ConfigureFunc = result.configure
	return result
}

type Backend struct {
	*schema.Backend

	// The fields below are set from configure
	ossClient *oss.Client

	bucketName           string
	statePath            string
	stateName            string
	serverSideEncryption bool
	acl                  string
	security_token       string
	endpoint             string
	lock                 bool
}

func (b *Backend) configure(ctx context.Context) error {
	if b.ossClient != nil {
		return nil
	}

	// Grab the resource data
	d := schema.FromContextBackendConfig(ctx)

	b.bucketName = d.Get("bucket").(string)
	dir := strings.Trim(d.Get("path").(string), "/")
	if strings.HasPrefix(dir, "./") {
		dir = strings.TrimPrefix(dir, "./")

	}

	b.statePath = dir
	b.stateName = d.Get("name").(string)
	b.serverSideEncryption = d.Get("encrypt").(bool)
	b.acl = d.Get("acl").(string)
	b.lock = d.Get("lock").(bool)

	access_key := d.Get("access_key").(string)
	secret_key := d.Get("secret_key").(string)
	security_token := d.Get("security_token").(string)
	endpoint := os.Getenv("OSS_ENDPOINT")
	if endpoint == "" {
		region := common.Region(d.Get("region").(string))
		if end, err := b.getOSSEndpointByRegion(access_key, secret_key, security_token, region); err != nil {
			return err
		} else {
			endpoint = end
		}
	}

	log.Printf("[DEBUG] Instantiate OSS client using endpoint: %#v", endpoint)
	var options []oss.ClientOption
	if security_token != "" {
		options = append(options, oss.SecurityToken(security_token))
	}
	options = append(options, oss.UserAgent(fmt.Sprintf("HashiCorp-Terraform-v%s", strings.TrimSuffix(terraform.VersionString(), "-dev"))))

	if client, err := oss.New(fmt.Sprintf("http://%s", endpoint), access_key, secret_key, options...); err != nil {
		return err
	} else {
		b.ossClient = client
	}

	return nil
}

func (b *Backend) getOSSEndpointByRegion(access_key, secret_key, security_token string, region common.Region) (string, error) {

	endpointClient := location.NewClient(access_key, secret_key)
	endpointClient.SetSecurityToken(security_token)
	endpoints, err := endpointClient.DescribeEndpoints(&location.DescribeEndpointsArgs{
		Id:          region,
		ServiceCode: "oss",
		Type:        "openAPI",
	})
	if err != nil {
		return "", fmt.Errorf("Describe endpoint using region: %#v got an error: %#v.", region, err)
	}
	endpointItem := endpoints.Endpoints.Endpoint
	endpoint := ""
	if endpointItem != nil && len(endpointItem) > 0 {
		endpoint = endpointItem[0].Endpoint
	} else {
		endpoint = fmt.Sprintf("oss-%s.aliyuncs.com", string(region))
	}

	return endpoint, nil
}
