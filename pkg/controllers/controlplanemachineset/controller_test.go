/*
Copyright 2022 Red Hat, Inc.

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

package controlplanemachineset

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	configv1 "github.com/openshift/api/config/v1"
	machinev1 "github.com/openshift/api/machine/v1"
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	"github.com/openshift/cluster-control-plane-machine-set-operator/pkg/test"
	"github.com/openshift/cluster-control-plane-machine-set-operator/pkg/test/resourcebuilder"
	corev1 "k8s.io/api/core/v1"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/envtest/komega"
)

var _ = Describe("With a running controller", func() {
	var mgrCancel context.CancelFunc
	var mgrDone chan struct{}

	var namespaceName string

	const operatorName = "control-plane-machine-set"

	var co *configv1.ClusterOperator

	BeforeEach(func() {
		By("Setting up a namespace for the test")
		ns := resourcebuilder.Namespace().WithGenerateName("control-plane-machine-set-controller-").Build()
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		namespaceName = ns.GetName()

		By("Setting up a manager and controller")
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme:             testScheme,
			MetricsBindAddress: "0",
			Port:               testEnv.WebhookInstallOptions.LocalServingPort,
			Host:               testEnv.WebhookInstallOptions.LocalServingHost,
			CertDir:            testEnv.WebhookInstallOptions.LocalServingCertDir,
		})
		Expect(err).ToNot(HaveOccurred(), "Manager should be able to be created")

		reconciler := &ControlPlaneMachineSetReconciler{
			Namespace:    namespaceName,
			OperatorName: operatorName,
		}
		Expect(reconciler.SetupWithManager(mgr)).To(Succeed(), "Reconciler should be able to setup with manager")

		By("Starting the manager")
		var mgrCtx context.Context
		mgrCtx, mgrCancel = context.WithCancel(context.Background())
		mgrDone = make(chan struct{})

		go func() {
			defer GinkgoRecover()
			defer close(mgrDone)

			Expect(mgr.Start(mgrCtx)).To(Succeed())
		}()

		// CVO will create a blank cluster operator for us before the operator starts.
		co = resourcebuilder.ClusterOperator().WithName(operatorName).Build()
		Expect(k8sClient.Create(ctx, co)).To(Succeed())
	})

	AfterEach(func() {
		By("Stopping the manager")
		mgrCancel()
		// Wait for the mgrDone to be closed, which will happen once the mgr has stopped
		<-mgrDone

		test.CleanupResources(ctx, cfg, k8sClient, namespaceName,
			&corev1.Node{},
			&configv1.ClusterOperator{},
			&machinev1beta1.Machine{},
			&machinev1.ControlPlaneMachineSet{},
		)
	})

	Context("when a new Control Plane Machine Set is created", func() {
		var cpms *machinev1.ControlPlaneMachineSet

		// Create the CPMS just before each test so that we can set up
		// various test cases in BeforeEach blocks.
		JustBeforeEach(func() {
			// The default CPMS should be sufficient for this test.
			cpms = resourcebuilder.ControlPlaneMachineSet().WithNamespace(namespaceName).Build()
			Expect(k8sClient.Create(ctx, cpms)).Should(Succeed())
		})

		PIt("should add the controlplanemachineset.machine.openshift.io finalizer", func() {
			Eventually(komega.Object(cpms)).Should(HaveField("ObjectMeta.Finalizers", ContainElement(controlPlaneMachineSetFinalizer)))
		})
	})

	Context("with an existing ControlPlaneMachineSet", func() {
		var cpms *machinev1.ControlPlaneMachineSet

		BeforeEach(func() {
			// The default CPMS should be sufficient for this test.
			cpms = resourcebuilder.ControlPlaneMachineSet().WithNamespace(namespaceName).Build()
			Expect(k8sClient.Create(ctx, cpms)).Should(Succeed())

			// To ensure that at least one reconcile happens, wait for the status to not be empty.
			Eventually(komega.Object(cpms)).Should(HaveField("Status.ObservedGeneration", Not(Equal(int64(0)))))
		})

		Context("if the finalizer is removed", func() {
			BeforeEach(func() {
				// Ensure the finalizer was already added
				Expect(komega.Object(cpms)()).Should(HaveField("ObjectMeta.Finalizers", ContainElement(controlPlaneMachineSetFinalizer)))

				// Remove the finalizer
				Eventually(komega.Update(cpms, func() {
					cpms.ObjectMeta.Finalizers = []string{}
				})).Should(Succeed())

				// CPMS should now have no finalizers, reflecting the state of the API.
				Expect(cpms.ObjectMeta.Finalizers).To(BeEmpty())
			})

			PIt("should re-add the controlplanemachineset.machine.openshift.io finalizer", func() {
				Eventually(komega.Object(cpms)).Should(HaveField("ObjectMeta.Finalizers", ContainElement(controlPlaneMachineSetFinalizer)))
			})
		})
	})
})

var _ = Describe("ensureFinalizer", func() {
	var namespaceName string
	var reconciler *ControlPlaneMachineSetReconciler
	var cpms *machinev1.ControlPlaneMachineSet
	var logger test.TestLogger

	const existingFinalizer = "existingFinalizer"

	BeforeEach(func() {
		By("Setting up a namespace for the test")
		ns := resourcebuilder.Namespace().WithGenerateName("control-plane-machine-set-controller-").Build()
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		namespaceName = ns.GetName()

		reconciler = &ControlPlaneMachineSetReconciler{
			Client:    k8sClient,
			Scheme:    testScheme,
			Namespace: namespaceName,
		}

		// The ControlPlaneMachineSet should already exist by the time we get here.
		By("Creating a ControlPlaneMachineSet")
		cpms = resourcebuilder.ControlPlaneMachineSet().WithNamespace(namespaceName).Build()
		cpms.ObjectMeta.Finalizers = []string{existingFinalizer}
		Expect(k8sClient.Create(ctx, cpms)).Should(Succeed())

		logger = test.NewTestLogger()
	})

	AfterEach(func() {
		test.CleanupResources(ctx, cfg, k8sClient, namespaceName,
			&machinev1.ControlPlaneMachineSet{},
		)
	})

	Context("when the finalizer does not exist", func() {
		var updatedFinalizer bool
		var err error

		BeforeEach(func() {
			updatedFinalizer, err = reconciler.ensureFinalizer(ctx, logger.Logger(), cpms)
		})

		It("does not error", func() {
			Expect(err).ToNot(HaveOccurred())
		})

		PIt("returns that it updated the finalizer", func() {
			Expect(updatedFinalizer).To(BeTrue())
		})

		PIt("sets an appropriate log line", func() {
			Expect(logger.Entries()).To(ConsistOf(
				test.LogEntry{
					Level:   2,
					Message: "Added finalizer to control plane machine set",
				},
			))
		})

		PIt("ensures the finalizer is set on the API", func() {
			Eventually(komega.Object(cpms)).Should(HaveField("ObjectMeta.Finalizers", ContainElement(controlPlaneMachineSetFinalizer)))
		})

		It("does not remove any existing finalizers", func() {
			Eventually(komega.Object(cpms)).Should(HaveField("ObjectMeta.Finalizers", ContainElement(existingFinalizer)))
		})
	})

	Context("when the finalizer already exists", func() {
		var updatedFinalizer bool
		var err error

		BeforeEach(func() {
			By("Adding the finalizer to the existing object")
			Eventually(komega.Update(cpms, func() {
				cpms.SetFinalizers(append(cpms.GetFinalizers(), controlPlaneMachineSetFinalizer))
			})).Should(Succeed())

			Eventually(komega.Object(cpms)).Should(HaveField("ObjectMeta.Finalizers", ConsistOf(controlPlaneMachineSetFinalizer, existingFinalizer)))

			updatedFinalizer, err = reconciler.ensureFinalizer(ctx, logger.Logger(), cpms)
		})

		It("does not error", func() {
			Expect(err).ToNot(HaveOccurred())
		})

		It("returns that it did not update the finalizer", func() {
			Expect(updatedFinalizer).To(BeFalse())
		})

		PIt("sets an appropriate log line", func() {
			Expect(logger.Entries()).To(ConsistOf(
				test.LogEntry{
					Level:   4,
					Message: "Finalizer already present on control plane machine set",
				},
			))
		})

		It("does not remove any existing finalizers", func() {
			Eventually(komega.Object(cpms)).Should(HaveField("ObjectMeta.Finalizers", ConsistOf(controlPlaneMachineSetFinalizer, existingFinalizer)))
		})
	})

	Context("when the finalizer already exists, but the input is stale", func() {
		var updatedFinalizer bool
		var err error

		BeforeEach(func() {
			By("Adding the finalizer to the existing object")
			originalCPMS := cpms.DeepCopy()
			Eventually(komega.Update(cpms, func() {
				cpms.SetFinalizers(append(cpms.GetFinalizers(), controlPlaneMachineSetFinalizer))
			})).Should(Succeed())

			Eventually(komega.Object(cpms)).Should(HaveField("ObjectMeta.Finalizers", ConsistOf(controlPlaneMachineSetFinalizer, existingFinalizer)))

			updatedFinalizer, err = reconciler.ensureFinalizer(ctx, logger.Logger(), originalCPMS)
		})

		PIt("should return a conflict error", func() {
			Expect(err).To(MatchError(ContainSubstring("TODO")))
		})

		It("returns that it did not update the finalizer", func() {
			Expect(updatedFinalizer).To(BeFalse())
		})

		PIt("does not log", func() {
			Expect(logger.Entries()).To(BeEmpty())
		})

		It("does not remove any existing finalizers", func() {
			Eventually(komega.Object(cpms)).Should(HaveField("ObjectMeta.Finalizers", ConsistOf(controlPlaneMachineSetFinalizer, existingFinalizer)))
		})
	})
})