Absolutely. Small correction: your starter is Go, not Python — which is good, because Go is the right language for this if you want to use HashiCorp’s native HCL parser/writer.

Below is a more polished CLI tool I’d build around your idea. It can:

* Recursively scan .tf files
* Sort top-level blocks alphabetically by block type + labels
* Match resource blocks by:
    * resource type: google_project_iam_member
    * resource name: foo
    * glob patterns: google_*, *_iam_*
* Remove matching blocks
* Move matching blocks into a single output file
* Dry-run changes
* Write in-place
* Create .bak files
* Preserve comments/tokens reasonably well through hclwrite

HashiCorp’s hclwrite package is specifically intended for generating HCL and making “surgical changes” while preserving unchanged formatting/comments better than a raw AST rewrite. ParseConfig returns a mutable *hclwrite.File, and File.Bytes() serializes it back out.  ￼

⸻

Suggested project layout

tfhcl/
  go.mod
  main.go

Initialize it:

mkdir tfhcl
cd tfhcl
go mod init github.com/you/tfhcl
go get github.com/hashicorp/hcl/v2

⸻

Usage examples

## Sort all Terraform files recursively

go run . \
  --root ./terraform \
  --op sort \
  --dry-run=false \
  --in-place=true

## Remove every matching resource block

Example: remove every google_project_iam_member resource:

go run . \
  --root ./terraform \
  --op remove \
  --block resource \
  --resource-type 'google_project_iam_member' \
  --dry-run=false \
  --in-place=true

## Remove by wildcard

go run . \
  --root ./terraform \
  --op remove \
  --block resource \
  --resource-type 'google_*_iam_*' \
  --dry-run=false \
  --in-place=true

## Remove one specific resource

go run . \
  --root ./terraform \
  --op remove \
  --block resource \
  --resource-type 'google_service_account' \
  --resource-name 'legacy_worker' \
  --dry-run=false \
  --in-place=true

Move all matching resources into one file

go run . \
  --root ./terraform/prod \
  --op move \
  --block resource \
  --resource-type 'google_project_iam_*' \
  --out ./terraform/prod/iam_migrated.tf \
  --dry-run=false \
  --in-place=true

⸻

# Important caveat

## This is good for top-level Terraform blocks like:

resource "x" "y" {}
module "foo" {}
data "x" "y" {}
variable "foo" {}
locals {}
terraform {}
provider "google" {}

It does not currently rewrite nested blocks, like nested dynamic, provisioner, lifecycle, or ingress blocks inside resources. That is doable, but I’d treat it as a separate feature because recursive edits need much stricter guardrails.

Also, sorting will rebuild the top-level body. hclwrite is designed to preserve unchanged formatting better than a plain AST rewrite, but any tool that clears/re-appends blocks can still normalize spacing around the touched parts. Use terraform fmt -recursive afterward.

⸻

What I’d add next

The next meaningful upgrade would be a selector syntax like this:

--select 'resource.google_project_iam_member.*'
--select 'module.network'
--select 'data.google_iam_policy.admin'

Then support operations like:

--set 'count=0'
--rename-label 'old_name=new_name'
--move-to iam.tf
--delete-attr 'lifecycle.ignore_changes'

For your real use case — large Terraform repos with risky bulk changes — I would also add a --plan mode that emits a markdown report before changing anything:

Matched 37 blocks across 18 files
resource.google_project_iam_member.foo
  file: prod/platform/iam.tf
  action: remove
resource.google_project_iam_member.bar
  file: nonprod/platform/iam.tf
  action: remove

That report is the difference between “handy script” and “tool you can safely use before opening an MR.”
