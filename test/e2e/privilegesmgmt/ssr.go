/*
Copyright the Velero contributors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package privilegesmgmt

import (
	"context"
	"flag"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	waitutil "k8s.io/apimachinery/pkg/util/wait"
	kbclient "sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/vmware-tanzu/velero/pkg/apis/velero/v1"
	. "github.com/vmware-tanzu/velero/test/e2e"
	. "github.com/vmware-tanzu/velero/test/e2e/util/k8s"
	. "github.com/vmware-tanzu/velero/test/e2e/util/velero"
)

func SSRTest() {
	testNS := "ssr-test"
	var (
		err error
	)

	BeforeEach(func() {
		flag.Parse()
		if VeleroCfg.InstallVelero {
			Expect(VeleroInstall(context.Background(), &VeleroCfg, false)).To(Succeed())
		}
	})

	AfterEach(func() {
		if VeleroCfg.InstallVelero {
			if !VeleroCfg.Debug {
				Expect(VeleroUninstall(context.Background(), VeleroCfg.VeleroCLI, VeleroCfg.VeleroNamespace)).To(Succeed())
			}
		}
	})

	It(fmt.Sprintf("Should create an ssr object in the %s namespace and later removed by controller", VeleroCfg.VeleroNamespace), func() {
		defer DeleteNamespace(context.TODO(), *VeleroCfg.ClientToInstallVelero, testNS, false)
		ctx, _ := context.WithTimeout(context.Background(), time.Minute*10)
		By(fmt.Sprintf("Create %s namespace", testNS))
		Expect(CreateNamespace(ctx, *VeleroCfg.ClientToInstallVelero, testNS)).To(Succeed(),
			fmt.Sprintf("Failed to create %s namespace", testNS))

		By(fmt.Sprintf("Get version in %s namespace", testNS), func() {
			Expect(VeleroVersion(context.Background(), VeleroCfg.VeleroCLI, testNS)).To(Succeed(),
				fmt.Sprintf("Failed to create an ssr object in the %s namespace", testNS))
		})
		By(fmt.Sprintf("Get version in %s namespace", VeleroCfg.VeleroNamespace), func() {
			Expect(VeleroVersion(context.Background(), VeleroCfg.VeleroCLI, VeleroCfg.VeleroNamespace)).To(Succeed(),
				fmt.Sprintf("Failed to create an ssr object in %s namespace", VeleroCfg.VeleroNamespace))
		})
		ssrListResp := new(v1.ServerStatusRequestList)
		By(fmt.Sprintf("Check ssr object in %s namespace", VeleroCfg.VeleroNamespace))
		err = waitutil.PollImmediate(5*time.Second, time.Minute,
			func() (bool, error) {
				if err = VeleroCfg.ClientToInstallVelero.Kubebuilder.List(ctx, ssrListResp, &kbclient.ListOptions{Namespace: VeleroCfg.VeleroNamespace}); err != nil {
					return false, fmt.Errorf("failed to list ssr object in %s namespace with err %v", VeleroCfg.VeleroNamespace, err)
				}
				if len(ssrListResp.Items) != 1 {
					return false, fmt.Errorf("count of ssr object in %s namespace is not 1", VeleroCfg.VeleroNamespace)
				}

				if ssrListResp.Items[0].Status.ServerVersion == "" {
					fmt.Printf("ServerVersion of ssr object in %s namespace should not empty, current response result %v\n", VeleroCfg.VeleroNamespace, ssrListResp)
					return false, nil
				}

				if ssrListResp.Items[0].Status.Phase != "Processed" {
					return false, fmt.Errorf("phase of ssr object in %s namespace should be Processed but got phase %s", VeleroCfg.VeleroNamespace, ssrListResp.Items[0].Status.Phase)
				}
				return true, nil
			})
		if err == waitutil.ErrWaitTimeout {
			fmt.Printf("exceed test case deadline and failed to check ssr object in %s namespace", VeleroCfg.VeleroNamespace)
		}
		Expect(err).To(Succeed(), fmt.Sprintf("Failed to check ssr object in %s namespace", VeleroCfg.VeleroNamespace))

		By(fmt.Sprintf("Check ssr object in %s namespace", testNS))
		Expect(VeleroCfg.ClientToInstallVelero.Kubebuilder.List(ctx, ssrListResp, &kbclient.ListOptions{Namespace: testNS})).To(Succeed(),
			fmt.Sprintf("Failed to list ssr object in %s namespace", testNS))
		Expect(len(ssrListResp.Items)).To(BeNumerically("==", 1),
			fmt.Sprintf("Count of ssr object in %s namespace is not 1", testNS))
		Expect(ssrListResp.Items[0].Status.Phase).To(BeEmpty(),
			fmt.Sprintf("Status of ssr object in %s namespace should be empty", testNS))
		Expect(ssrListResp.Items[0].Status.ServerVersion).To(BeEmpty(),
			fmt.Sprintf("ServerVersion of ssr object in %s namespace should be empty", testNS))

		By(fmt.Sprintf("Waiting ssr object in %s namespace deleted", VeleroCfg.VeleroNamespace))
		err = waitutil.PollImmediateInfinite(5*time.Second,
			func() (bool, error) {
				if err = VeleroCfg.ClientToInstallVelero.Kubebuilder.List(ctx, ssrListResp, &kbclient.ListOptions{Namespace: VeleroCfg.VeleroNamespace}); err != nil {
					if apierrors.IsNotFound(err) {
						return true, nil
					}
					return false, err
				}
				if len(ssrListResp.Items) != 0 {
					return false, nil
				}
				return true, nil
			})

		Expect(err).To(Succeed(), fmt.Sprintf("ssr object in %s namespace is not been deleted by controller", VeleroCfg.VeleroNamespace))
	})
}
