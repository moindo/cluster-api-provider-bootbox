# Fork of cozystack v1.7.0 packages/apps/kubernetes-nodes with the bootbox
# substrate. values.schema.json is maintained by hand here (upstream generates
# it with cozyvalues-gen).
.PHONY: lint template
lint:
	helm lint . --values tests/values-bootbox.yaml
template:
	helm template baremetal-nodes-training-gpu . --namespace tenant-training --values tests/values-bootbox.yaml
