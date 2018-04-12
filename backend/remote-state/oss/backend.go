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
			"bucket": &schema.Schema{
				Type:        schema.TypeString,
				Required:    true,
				Description: "The name of the OSS bucket",
			},

			"key": &schema.Schema{
				Type:        schema.TypeString,
				Required:    true,
				Description: "The path to the state file inside the bucket",
				ValidateFunc: func(v interface{}, s string) ([]string, []error) {
					// oss will strip leading slashes from an object, so while this will
					// technically be accepted by oss, it will break our workspace hierarchy.
					if strings.HasPrefix(v.(string), "/") {
						return nil, []error{fmt.Errorf("key must not start with '/'")}
					}
					return nil, nil
				},
			},
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
				Required:    true,
				Description: "The region of the OSS bucket. It will be ignored when 'endpoint' is specified.",
				DefaultFunc: schema.EnvDefaultFunc("ALICLOUD_REGION", os.Getenv("ALICLOUD_DEFAULT_REGION")),
			},

			"endpoint": &schema.Schema{
				Type:        schema.TypeString,
				Optional:    true,
				Description: "A custom endpoint for the OSS API",
				DefaultFunc: schema.EnvDefaultFunc("ALICLOUD_OSS_ENDPOINT", ""),
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
			},

			"workspace_key_prefix": &schema.Schema{
				Type:        schema.TypeString,
				Optional:    true,
				Description: "The prefix applied to the non-default state path inside the bucket",
				Default:     "workspaces",
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
	keyName              string
	serverSideEncryption bool
	acl                  string
	security_token       string
	endpoint             string
	workspaceKeyPrefix   string
}

func (b *Backend) configure(ctx context.Context) error {
	if b.ossClient != nil {
		return nil
	}

	// Grab the resource data
	data := schema.FromContextBackendConfig(ctx)

	b.bucketName = data.Get("bucket").(string)
	b.keyName = data.Get("key").(string)
	b.serverSideEncryption = data.Get("encrypt").(bool)
	b.acl = data.Get("acl").(string)
	b.workspaceKeyPrefix = data.Get("workspace_key_prefix").(string)
	access_key := data.Get("access_key").(string)
	secret_key := data.Get("secret_key").(string)
	security_token := data.Get("security_token").(string)
	endpoint := data.Get("endpoint").(string)
	if endpoint == "" {
		region := common.Region(data.Get("region").(string))
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
	options = append(options, oss.UserAgent(fmt.Sprintf("HashiCorp-Terraform-v%s", terraform.VersionString())))

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
	}

	return endpoint, nil
}
