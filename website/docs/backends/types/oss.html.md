---
layout: "backend-types"
page_title: "Backend Type: oss"
sidebar_current: "docs-backends-types-standard-oss"
description: |-
  Terraform can store state remotely in OSS and lock that state with OSS.
---

# OSS

**Kind: Standard (with locking via OSS)**

Stores the state as a given key in a given bucket on Stores
[Alibaba Cloud OSS](https://www.alibabacloud.com/help/product/31815.htm).
This backend also supports state locking and consistency checking via Alibaba Cloud OSS.


## Example Configuration

```hcl
terraform {
  backend "oss" {
    bucket = "bucket-for-terraform-state"
    key    = "path/mystate"
    region = "cn-beijing"
  }
}
```

This assumes we have a bucket created called `bucket-for-terraform-state`. The
Terraform state is written to the key `path/mystate`.


## Using the OSS remote state

To make use of the OSS remote state we can use the
[`terraform_remote_state` data
source](/docs/providers/terraform/d/remote_state.html).

```hcl
terraform {
  backend "oss" {
    bucket = "remote-state-dns"
    key    = "mystate/state"
    region = "cn-beijing"
    workspace_key_prefix = "workspace"
  }
}
```

The `terraform_remote_state` data source will return all of the root outputs
defined in the referenced remote state, an example output might look like:

```
terraform_remote_state.dns
    id                          = 2017-12-15 09:13:55.309409899 +0000 UTC
    backend                     = oss
    config.%                    = 4
    config.bucket               = remote-state-dns
    config.key                  = mystate
    config.region               = cn-beijing
    config.workspace_key_prefix = space
    environment                 = default
    workspace                   = default
```

## Configuration variables

The following configuration options or environment variables are supported:

 * `bucket` - (Required) The name of the OSS bucket.
 * `key` - (Required) The path to the state file inside the bucket. When using
   a non-default [workspace](/docs/state/workspaces.html), the state path will
   be `/workspace_key_prefix/workspace_name/key`
 * `region` / `ALICLOUD_REGION` - (Optional) The region of the OSS
 bucket. Default to "cn-beijing". It will be ignored when `endpoint` is specified.
 * `endpoint` / `ALICLOUD_OSS_ENDPOINT` - (Optional) A custom endpoint for the
 OSS API.
 * `encrypt` - (Optional) Whether to enable server side
   encryption of the state file. If it is true, OSS will use 'AES256' encryption algorithm to encrypt state file.
 * `acl` - [Object
   ACL](https://www.alibabacloud.com/help/doc-detail/52284.htm)
   to be applied to the state file.
 * `access_key` / `ALICLOUD_ACCESS_KEY` - (Optional) Alicloud access key.
 * `secret_key` / `ALICLOUD_SECRET_KEY` - (Optional) Alicloud secret access key.
 * `security_token` - (Optional) STS access token. It can also be
   sourced from the `ALICLOUD_SECURITY_TOKEN` environment variable.
 * `workspace_key_prefix` - (Optional) The prefix applied to the state path
   inside the bucket. This is only relevant when using a non-default workspace.
   This defaults to "workspaces"

