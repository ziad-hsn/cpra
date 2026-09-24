# CSI fixture for disposable test clusters

This is the Kubernetes CSI project's **non-production** hostpath driver, restricted to one dedicated `kind-cpra-*` node. It tests CPRa's CSI/RWOP integration and filesystem behavior on that test node. It does not qualify a production cloud disk, network filesystem, failover behavior, or a storage account. The driver requires privileged mounts inside the disposable kind node. No part of this fixture belongs in the CPRa application image or production chart.

The manifest is derived from [csi-driver-host-path commit c7383ef](https://github.com/kubernetes-csi/csi-driver-host-path/tree/c7383ef90bae7803195ede035a7d85ab3c7cffc9/deploy/kubernetes-latest/hostpath) and [external-provisioner commit 12a344a](https://github.com/kubernetes-csi/external-provisioner/blob/12a344a40072d655cb9374a93483305f1a1be557/deploy/kubernetes/rbac.yaml), under Apache 2.0 (included in `LICENSE`). It retains the hostpath, node registration, liveness and provisioning containers. Image indexes are pinned by digest. No snapshot, resize, attach or external health-monitor controller is required by this fixture, and those features are not tested here. `attachRequired=false` is explicit. The provisioner has no leader election because exactly one instance runs.

```sh
python3 scripts/packaging/install_csi_fixture.py \
  --kubeconfig /private/cpra-test-kubeconfig --context kind-cpra-packaging-test
python3 scripts/packaging/live_helm.py --image cpra:candidate \
  --helm bin/release-tools/helm3 --upgrade-helm bin/release-tools/helm4 \
  --kubeconfig /private/cpra-test-kubeconfig --context kind-cpra-packaging-test \
  --namespace cpra-rwop-test --access-mode ReadWriteOncePod \
  --storage-class cpra-csi-hostpath --out evidence/local/cpra-rwop.json
```

The harness creates a second pod requesting the live state claim and requires an explicit RWOP scheduling rejection, then confirms the original owner remains ready. Its report records the PV, driver, filesystem, versions and rejection. Namespace cleanup deletes only test claims; remove the dedicated kind cluster after the campaign to remove fixture permissions and node-local data.
