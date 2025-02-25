package hncconfig_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/rbac/v1"
	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	api "sigs.k8s.io/hierarchical-namespaces/api/v1alpha2"
	. "sigs.k8s.io/hierarchical-namespaces/internal/integtest"
)

const (
	// nopTime is the time we're willing to wait to ensure that something _hasn't_ happened
	// ("no-operation time"). If we expect something *to* happen, then we use Eventually().
	//
	// From experiments it takes ~0.015s for HNC to propagate an object. Setting the sleep time to 1s
	// should be long enough.
	//
	// We may need to increase the sleep time in future if HNC takes longer to propagate objects.
	nopTime = 1 * time.Second

	// countUpdateTime is the timeout for `Eventually` to verify the object counts in the HNC Config
	// status. We've set the duration to be 1s for tests (it's usually much higher) but sync Prow
	// seems to get overloaded easily, we'll keep the timeout much higher - aludwin, Mar 2022
	countUpdateTime = 5 * time.Second

	// testModeMisssing is a fake mode to indicate that the spec/status doesn't exist in the config
	testModeMisssing api.SynchronizationMode = "<missing>"
)

func TestInteg(t *testing.T) {
	HNCRun(t, "HNCConfig reconciler")
}

var _ = BeforeSuite(HNCBeforeSuite)
var _ = AfterSuite(HNCAfterSuite)

var _ = Describe("HNCConfiguration", func() {
	ctx := context.Background()

	var (
		fooName string
		barName string
	)

	BeforeEach(func() {
		ResetHNCConfigToDefault(ctx)
		// We want to ensure we're working with a clean slate, in case a previous tests objects still exist
		CleanupObjects(ctx)

		fooName = CreateNS(ctx, "foo")
		barName = CreateNS(ctx, "bar")
	})

	AfterEach(func() {
		// Change current config back to the default value.
		ResetHNCConfigToDefault(ctx)
		CleanupObjects(ctx)
	})

	It("should have empty spec, and Roles and RoleBindings in propagate mode in status by default", func() {
		Eventually(typeSpecMode(ctx, api.RBACGroup, api.RoleResource)).Should(Equal(testModeMisssing))
		Eventually(typeSpecMode(ctx, api.RBACGroup, api.RoleBindingResource)).Should(Equal(testModeMisssing))
		Eventually(typeStatusMode(ctx, api.RBACGroup, api.RoleResource)).Should(Equal(api.Propagate))
		Eventually(typeStatusMode(ctx, api.RBACGroup, api.RoleBindingResource)).Should(Equal(api.Propagate))
	})

	It("should propagate `roles` by default", func() {
		SetParent(ctx, barName, fooName)
		// Give foo a role.
		MakeObject(ctx, api.RoleResource, fooName, "foo-role")

		Eventually(HasObject(ctx, api.RoleResource, barName, "foo-role")).Should(BeTrue())
		Expect(ObjectInheritedFrom(ctx, api.RoleResource, barName, "foo-role")).Should(Equal(fooName))
	})

	It("should ignore the `roles` configuration in the spec and set `MultipleConfigurationsForType` condition", func() {
		AddToHNCConfig(ctx, api.RBACGroup, api.RoleResource, api.Ignore)

		Eventually(typeSpecMode(ctx, api.RBACGroup, api.RoleResource)).Should(Equal(api.Ignore))
		Eventually(typeStatusMode(ctx, api.RBACGroup, api.RoleResource)).Should(Equal(api.Propagate))
		Eventually(GetHNCConfigCondition(ctx, api.ConditionBadTypeConfiguration, api.ReasonMultipleConfigsForType)).Should(ContainSubstring(api.RoleResource))
	})

	It("should propagate RoleBindings by default", func() {
		SetParent(ctx, barName, fooName)
		// Give foo a role binding.
		makeRoleBinding(ctx, fooName, "foo-role", "foo-admin", "foo-role-binding")

		Eventually(HasObject(ctx, api.RoleBindingResource, barName, "foo-role-binding")).Should(BeTrue())
		Expect(ObjectInheritedFrom(ctx, api.RoleBindingResource, barName, "foo-role-binding")).Should(Equal(fooName))
	})

	It("should ignore the `rolebindings` configuration in the spec and set `MultipleConfigurationsForType` condition", func() {
		AddToHNCConfig(ctx, api.RBACGroup, api.RoleBindingResource, api.Ignore)

		Eventually(typeSpecMode(ctx, api.RBACGroup, api.RoleBindingResource)).Should(Equal(api.Ignore))
		Eventually(typeStatusMode(ctx, api.RBACGroup, api.RoleBindingResource)).Should(Equal(api.Propagate))
		Eventually(GetHNCConfigCondition(ctx, api.ConditionBadTypeConfiguration, api.ReasonMultipleConfigsForType)).Should(ContainSubstring(api.RoleBindingResource))
	})

	It("should unset ResourceNotFound condition if a bad type spec is removed", func() {
		// Group of ConfigMap should be ""
		AddToHNCConfig(ctx, "wrong", "configmaps", api.Propagate)

		Eventually(GetHNCConfigCondition(ctx, api.ConditionBadTypeConfiguration, api.ReasonResourceNotFound)).Should(ContainSubstring("configmaps"))

		removeTypeConfig(ctx, "wrong", "configmaps")

		Eventually(GetHNCConfigCondition(ctx, api.ConditionBadTypeConfiguration, api.ReasonResourceNotFound)).Should(Equal(""))
	})

	It("should set MultipleConfigurationsForType if there are multiple configurations for one type", func() {
		// Add multiple configurations for a type.
		AddToHNCConfig(ctx, "", "secrets", api.Propagate)
		AddToHNCConfig(ctx, "", "secrets", api.Remove)

		// The first configuration should be applied.
		Eventually(GetHNCConfigCondition(ctx, api.ConditionBadTypeConfiguration, api.ReasonMultipleConfigsForType)).
			Should(ContainSubstring("Multiple sync mode settings found for \"secrets\"; all but one (%q) will be ignored", api.Propagate))
	})

	It("should unset MultipleConfigurationsForType if extra configurations are later removed", func() {
		// Add multiple configurations for a type.
		AddToHNCConfig(ctx, "", "secrets", api.Propagate)
		AddToHNCConfig(ctx, "", "secrets", api.Remove)

		Eventually(GetHNCConfigCondition(ctx, api.ConditionBadTypeConfiguration, api.ReasonMultipleConfigsForType)).
			Should(ContainSubstring("Multiple sync mode settings found for \"secrets\"; all but one (%q) will be ignored", api.Propagate))

		removeTypeConfigWithMode(ctx, "", "secrets", api.Remove)

		Eventually(GetHNCConfigCondition(ctx, api.ConditionBadTypeConfiguration, api.ReasonMultipleConfigsForType)).
			ShouldNot(ContainSubstring("Group: , Resource: secrets, Mode: %s", api.Remove))
	})

	It("should not propagate objects if the type is not in HNCConfiguration", func() {
		SetParent(ctx, barName, fooName)
		MakeObject(ctx, "resourcequotas", fooName, "foo-resource-quota")

		// Foo should have "foo-resource-quota" since we created there.
		Eventually(HasObject(ctx, "resourcequotas", fooName, "foo-resource-quota")).Should(BeTrue())
		// Sleep to give "foo-resource-quota" a chance to propagate from foo to bar, if it could.
		time.Sleep(nopTime)
		Expect(HasObject(ctx, "resourcequotas", barName, "foo-resource-quota")()).Should(BeFalse())
	})

	It("should propagate objects if the mode of a type is set to propagate", func() {
		AddToHNCConfig(ctx, "", "secrets", api.Propagate)

		SetParent(ctx, barName, fooName)
		MakeObject(ctx, "secrets", fooName, "foo-sec")

		// Foo should have "foo-sec" since we created there.
		Eventually(HasObject(ctx, "secrets", fooName, "foo-sec")).Should(BeTrue())
		// "foo-sec" should now be propagated from foo to bar.
		Eventually(HasObject(ctx, "secrets", barName, "foo-sec")).Should(BeTrue())
		Expect(ObjectInheritedFrom(ctx, "secrets", barName, "foo-sec")).Should(Equal(fooName))
	})

	It("should stop propagating objects if the mode of a type is changed to ignore", func() {
		AddToHNCConfig(ctx, "", "secrets", api.Propagate)

		SetParent(ctx, barName, fooName)
		MakeObject(ctx, "secrets", fooName, "foo-sec")

		// Foo should have "foo-sec" since we created there.
		Eventually(HasObject(ctx, "secrets", fooName, "foo-sec")).Should(BeTrue())
		// "foo-sec" should now be propagated from foo to bar because we set the mode of Secret to "propagate".
		Eventually(HasObject(ctx, "secrets", barName, "foo-sec")).Should(BeTrue())
		Expect(ObjectInheritedFrom(ctx, "secrets", barName, "foo-sec")).Should(Equal(fooName))

		// Change to ignore and wait for reconciler
		setTypeConfig(ctx, "", "secrets", api.Ignore)

		bazName := CreateNS(ctx, "baz")
		SetParent(ctx, bazName, fooName)
		// Sleep to give "foo-sec" a chance to propagate from foo to baz, if it could.
		time.Sleep(nopTime)
		Expect(HasObject(ctx, "secrets", bazName, "foo-sec")()).Should(BeFalse())
	})

	It("should propagate objects if the mode of a type is changed from ignore to propagate", func() {
		AddToHNCConfig(ctx, "", "resourcequotas", api.Ignore)

		SetParent(ctx, barName, fooName)
		MakeObject(ctx, "resourcequotas", fooName, "foo-resource-quota")

		// Foo should have "foo-resource-quota" since we created there.
		Eventually(HasObject(ctx, "resourcequotas", fooName, "foo-resource-quota")).Should(BeTrue())
		// Sleep to give "foo-resource-quota" a chance to propagate from foo to bar, if it could.
		time.Sleep(nopTime)
		Expect(HasObject(ctx, "resourcequotas", barName, "foo-resource-quota")()).Should(BeFalse())

		setTypeConfig(ctx, "", "resourcequotas", api.Propagate)
		// "foo-resource-quota" should now be propagated from foo to bar because the mode of ResourceQuota is set to "propagate".
		Eventually(HasObject(ctx, "resourcequotas", barName, "foo-resource-quota")).Should(BeTrue())
		Expect(ObjectInheritedFrom(ctx, "resourcequotas", barName, "foo-resource-quota")).Should(Equal(fooName))
	})

	It("should remove propagated objects if the mode of a type is changed from propagate to remove", func() {
		AddToHNCConfig(ctx, "", "secrets", api.Propagate)
		SetParent(ctx, barName, fooName)
		MakeObject(ctx, "secrets", fooName, "foo-sec")

		Eventually(HasObject(ctx, "secrets", fooName, "foo-sec")).Should(BeTrue())
		// "foo-sec" should be propagated from foo to bar.
		Eventually(HasObject(ctx, "secrets", barName, "foo-sec")).Should(BeTrue())
		Expect(ObjectInheritedFrom(ctx, "secrets", barName, "foo-sec")).Should(Equal(fooName))

		setTypeConfig(ctx, "", "secrets", api.Remove)

		// Foo should still have "foo-sec" because it is a source object, not propagated one.
		// Therefore, we do not remove it.
		Eventually(HasObject(ctx, "secrets", fooName, "foo-sec")).Should(BeTrue())
		// "foo-sec" should be removed from bar.
		Eventually(HasObject(ctx, "secrets", barName, "foo-sec")).Should(BeFalse())
	})

	It("should propagate objects if the mode of a type is changed from remove to propagate", func() {
		AddToHNCConfig(ctx, "", "resourcequotas", api.Remove)
		SetParent(ctx, barName, fooName)
		MakeObject(ctx, "resourcequotas", fooName, "foo-resource-quota")

		// Foo should have "foo-resource-quota" because it is a source object, which will not be removed.
		Eventually(HasObject(ctx, "resourcequotas", fooName, "foo-resource-quota")).Should(BeTrue())
		// Sleep to give "foo-resource-quota" a chance to propagate from foo to bar, if it could.
		time.Sleep(nopTime)
		// "foo-resource-quota" should not be propagated from foo to bar.
		Expect(HasObject(ctx, "resourcequotas", barName, "foo-resource-quota")()).Should(BeFalse())

		setTypeConfig(ctx, "", "resourcequotas", api.Propagate)

		// "foo-resource-quota" should be propagated from foo to bar.
		Eventually(HasObject(ctx, "resourcequotas", barName, "foo-resource-quota")).Should(BeTrue())
		Expect(ObjectInheritedFrom(ctx, "resourcequotas", barName, "foo-resource-quota")).Should(Equal(fooName))
	})

	It("should stop propagating objects if a type is first set to propagate mode then removed from the spec", func() {
		AddToHNCConfig(ctx, "", "secrets", api.Propagate)
		SetParent(ctx, barName, fooName)
		MakeObject(ctx, "secrets", fooName, "foo-sec")

		// "foo-sec" should propagate from foo to bar.
		Eventually(HasObject(ctx, "secrets", barName, "foo-sec")).Should(BeTrue())
		Expect(ObjectInheritedFrom(ctx, "secrets", barName, "foo-sec")).Should(Equal(fooName))

		// Remove from spec and wait for the reconciler to pick it up
		removeTypeConfig(ctx, "", "secrets")
		Eventually(typeStatusMode(ctx, "", "secrets")).Should(Equal(testModeMisssing))

		// Give foo another secret.
		MakeObject(ctx, "secrets", fooName, "foo-sec-2")
		// Foo should have "foo-sec-2" because we created it there.
		Eventually(HasObject(ctx, "secrets", fooName, "foo-sec-2")).Should(BeTrue())
		// "foo-sec-2" should not propagate from foo to bar because the reconciliation request is ignored.
		Consistently(HasObject(ctx, "secrets", barName, "foo-sec-2")()).Should(BeFalse(), "foo-sec-2 should not propagate to %s because propagation's been disabled", barName)

	})

	It("should reconcile after adding a new crd to the apiserver", func() {
		// Add a config for a type that hasn't been defined yet.
		AddToHNCConfig(ctx, "stable.example.com", "crontabs", api.Propagate)

		// The corresponding object reconciler should not be created because the type does not exist.
		Eventually(GetHNCConfigCondition(ctx, api.ConditionBadTypeConfiguration, api.ReasonResourceNotFound)).
			Should(ContainSubstring("crontabs"))

		// Add the CRD for CronTab to the apiserver.
		createCronTabCRD(ctx)

		// The object reconciler for CronTab should be created successfully, which means all conditions
		// should be cleared.
		Eventually(GetHNCConfigCondition(ctx, api.ConditionBadTypeConfiguration, api.ReasonResourceNotFound)).Should(Equal(""))

		// Give foo a CronTab object.
		SetParent(ctx, barName, fooName)
		MakeObject(ctx, "crontabs", fooName, "foo-crontab")

		// "foo-crontab" should be propagated from foo to bar.
		Eventually(HasObject(ctx, "crontabs", barName, "foo-crontab")).Should(BeTrue())
		Expect(ObjectInheritedFrom(ctx, "crontabs", barName, "foo-crontab")).Should(Equal(fooName))
	})

	It("manager should not panic and ignore wrong Clusterscoped type setting in HNCConfiguration", func() {
		// Add a config for a type that hasn't been defined yet.
		AddToHNCConfig(ctx, api.RBACGroup, "clusterroles", api.Propagate)

		Eventually(GetHNCConfigCondition(ctx, api.ConditionBadTypeConfiguration, api.ReasonResourceNotNamespaced)).
			Should(ContainSubstring("resource \"clusterroles.rbac.authorization.k8s.io\" is not namespaced"))
	})

	It("should set NumPropagatedObjects back to 0 after deleting the source object in propagate mode", func() {
		AddToHNCConfig(ctx, "", "limitranges", api.Propagate)
		SetParent(ctx, barName, fooName)
		MakeObject(ctx, "limitranges", fooName, "foo-lr")

		Eventually(getNumPropagatedObjects(ctx, "", "limitranges"), countUpdateTime).Should(Equal(1))

		DeleteObject(ctx, "limitranges", fooName, "foo-lr")

		Eventually(getNumPropagatedObjects(ctx, "", "limitranges"), countUpdateTime).Should(Equal(0))
	})

	It("should set NumPropagatedObjects back to 0 after switching from propagate to remove mode", func() {
		AddToHNCConfig(ctx, "", "limitranges", api.Propagate)
		SetParent(ctx, barName, fooName)
		MakeObject(ctx, "limitranges", fooName, "foo-lr")

		Eventually(getNumPropagatedObjects(ctx, "", "limitranges"), countUpdateTime).Should(Equal(1))

		setTypeConfig(ctx, "", "limitranges", api.Remove)

		Eventually(getNumPropagatedObjects(ctx, "", "limitranges"), countUpdateTime).Should(Equal(0))

		// TODO: Delete objects created via MakeObject after each test case.
		DeleteObject(ctx, "limitranges", fooName, "foo-lr")
	})

	It("should set NumSourceObjects for a type in propagate mode", func() {
		AddToHNCConfig(ctx, "", "limitranges", api.Propagate)
		MakeObject(ctx, "limitranges", fooName, "foo-lr")

		Eventually(getNumSourceObjects(ctx, "", "limitranges"), countUpdateTime).Should(Equal(1))

		// TODO: Delete objects created via MakeObject after each test case.
		DeleteObject(ctx, "limitranges", fooName, "foo-lr")
	})

	// If a mode is unset, it is treated as `propagate` by default, in which case we will also compute NumSourceObjects
	It("should set NumSourceObjects for a type with unset mode", func() {
		AddToHNCConfig(ctx, "", "limitranges", "")
		MakeObject(ctx, "limitranges", fooName, "foo-lr")

		Eventually(getNumSourceObjects(ctx, "", "limitranges"), countUpdateTime).Should(Equal(1))

		// TODO: Delete objects created via MakeObject after each test case.
		DeleteObject(ctx, "limitranges", fooName, "foo-lr")
	})

	It("should decrement NumSourceObjects correctly after deleting an object of a type in propagate mode", func() {
		AddToHNCConfig(ctx, "", "limitranges", api.Propagate)
		MakeObject(ctx, "limitranges", fooName, "foo-lr")

		Eventually(getNumSourceObjects(ctx, "", "limitranges"), countUpdateTime).Should(Equal(1))

		DeleteObject(ctx, "limitranges", fooName, "foo-lr")

		Eventually(getNumSourceObjects(ctx, "", "limitranges"), countUpdateTime).Should(Equal(0))
	})

	It("should not set NumSourceObjects for a type not in propagate mode", func() {
		AddToHNCConfig(ctx, "", "limitranges", api.Remove)

		Eventually(hasNumSourceObjects(ctx, "", "limitranges"), countUpdateTime).Should(BeFalse())
	})
})

func typeSpecMode(ctx context.Context, group, resource string) func() api.SynchronizationMode {
	return func() api.SynchronizationMode {
		config, err := GetHNCConfig(ctx)
		if err != nil {
			return (api.SynchronizationMode)(err.Error())
		}
		for _, t := range config.Spec.Resources {
			if t.Group == group && t.Resource == resource {
				return t.Mode
			}
		}
		return testModeMisssing
	}
}

func typeStatusMode(ctx context.Context, group, resource string) func() api.SynchronizationMode {
	return func() api.SynchronizationMode {
		config, err := GetHNCConfig(ctx)
		if err != nil {
			return (api.SynchronizationMode)(err.Error())
		}
		for _, t := range config.Status.Resources {
			if t.Group == group && t.Resource == resource {
				return t.Mode
			}
		}
		return testModeMisssing
	}
}

// We cannot use `MakeObject` to create Rolebinding objects because `RoleRef` is a required field.
func makeRoleBinding(ctx context.Context, nsName, roleName, userName, roleBindingName string) {
	roleBinding := &v1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      roleBindingName,
			Namespace: nsName,
		},
		Subjects: []v1.Subject{
			{
				Kind: "User",
				Name: userName,
			},
		},
		RoleRef: v1.RoleRef{
			APIGroup: api.RBACGroup,
			Kind:     api.RoleKind,
			Name:     roleName,
		},
	}
	ExpectWithOffset(1, K8sClient.Create(ctx, roleBinding)).Should(Succeed())
}

func GetHNCConfigCondition(ctx context.Context, tp, reason string) func() string {
	return getNamedHNCConfigCondition(ctx, api.HNCConfigSingleton, tp, reason)
}

func getNamedHNCConfigCondition(ctx context.Context, nm, tp, reason string) func() string {
	return func() string {
		c, err := GetHNCConfigWithName(ctx, nm)
		if err != nil {
			return err.Error()
		}
		msg := ""
		for _, cond := range c.Status.Conditions {
			if cond.Type == tp && cond.Reason == reason {
				msg += cond.Message + "\n"
			}
		}
		return msg
	}
}

// setTypeConfig is usually what you should call. It updates the config *and* waits for the
// config to take effect, as shown by the status.
//
// If you're making a change that won't be reflected in the status (e.g. removing Role), call
// updateTypeConfig, which doesn't confirm that the mode's taken effect.
func setTypeConfig(ctx context.Context, group, resource string, mode api.SynchronizationMode) {
	updateTypeConfigWithOffset(ctx, 1, group, resource, mode)
	EventuallyWithOffset(1, typeStatusMode(ctx, group, resource)).Should(Equal(mode), "While setting type config for %s/%s to %s", group, resource, mode)
}

// updateTypeConfig is like setTypeConfig but it doesn't wait to confirm that the change was
// successful.
//lint:ignore U1000 Ignore for now, as it may be used again in the future
func updateTypeConfig(ctx context.Context, group, resource string, mode api.SynchronizationMode) {
	updateTypeConfigWithOffset(ctx, 1, group, resource, mode)
}

func updateTypeConfigWithOffset(ctx context.Context, offset int, group, resource string, mode api.SynchronizationMode) {
	EventuallyWithOffset(offset+1, func() error {
		// Get the existing spec from the apiserver
		c, err := GetHNCConfig(ctx)
		if err != nil {
			return err
		}

		// Modify the existing spec. We should find the thing we were looking for.
		found := false
		for i := 0; i < len(c.Spec.Resources); i++ { // don't use range-for since that creates copies of the objects
			if c.Spec.Resources[i].Group == group && c.Spec.Resources[i].Resource == resource {
				c.Spec.Resources[i].Mode = mode
				found = true
				break
			}
		}
		Expect(found).Should(BeTrue())

		// Update the apiserver
		GinkgoT().Logf("Changing type config of %s/%s to %s", group, resource, mode)
		return UpdateHNCConfig(ctx, c)
	}).Should(Succeed(), "While updating type config for %s/%s to %s", group, resource, mode)
}

//lint:ignore U1000 Ignore for now, as it may be used again in the future
func unsetTypeConfig(ctx context.Context, group, resource string) {
	removeTypeConfigWithOffset(1, ctx, group, resource)
	Eventually(typeStatusMode(ctx, group, resource)).Should(Equal(testModeMisssing))
}

func removeTypeConfig(ctx context.Context, group, resource string) {
	removeTypeConfigWithOffset(1, ctx, group, resource)
}

func removeTypeConfigWithOffset(offset int, ctx context.Context, group, resource string) {
	EventuallyWithOffset(offset+1, func() error {
		c, err := GetHNCConfig(ctx)
		if err != nil {
			return err
		}
		i := 0
		for ; i < len(c.Spec.Resources); i++ {
			if c.Spec.Resources[i].Group == group && c.Spec.Resources[i].Resource == resource {
				break
			}
		}
		// The type does not exist. Nothing to remove.
		if i == len(c.Spec.Resources) {
			return nil
		}
		GinkgoT().Logf("Removing type config for %s/%s", group, resource)
		c.Spec.Resources[i] = c.Spec.Resources[len(c.Spec.Resources)-1]
		c.Spec.Resources = c.Spec.Resources[:len(c.Spec.Resources)-1]
		return UpdateHNCConfig(ctx, c)
	}).Should(Succeed(), "While removing type config for %s/%s", group, resource)
}

func removeTypeConfigWithMode(ctx context.Context, group, resource string, mode api.SynchronizationMode) {
	EventuallyWithOffset(1, func() error {
		c, err := GetHNCConfig(ctx)
		if err != nil {
			return err
		}
		i := 0
		for ; i < len(c.Spec.Resources); i++ {
			if c.Spec.Resources[i].Group == group && c.Spec.Resources[i].Resource == resource && c.Spec.Resources[i].Mode == mode {
				break
			}
		}
		// The type does not exist. Nothing to remove.
		if i == len(c.Spec.Resources) {
			return nil
		}
		c.Spec.Resources[i] = c.Spec.Resources[len(c.Spec.Resources)-1]
		c.Spec.Resources = c.Spec.Resources[:len(c.Spec.Resources)-1]
		return UpdateHNCConfig(ctx, c)
	}).Should(Succeed(), "While removing type config for %s/%s with mode %s", group, resource, mode)
}

func createCronTabCRD(ctx context.Context) {
	crontab := apiextensions.CustomResourceDefinition{
		TypeMeta: metav1.TypeMeta{
			Kind:       "CustomResourceDefinition",
			APIVersion: "apiextensions.k8s.io/v1"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "crontabs.stable.example.com",
		},
		Spec: apiextensions.CustomResourceDefinitionSpec{
			Group: "stable.example.com",
			Versions: []apiextensions.CustomResourceDefinitionVersion{
				{Name: "v1", Served: true, Storage: true, Schema: &apiextensions.CustomResourceValidation{
					OpenAPIV3Schema: &apiextensions.JSONSchemaProps{
						Type: "object",
					}},
				},
			},
			Names: apiextensions.CustomResourceDefinitionNames{
				Singular: "crontab",
				Plural:   "crontabs",
				Kind:     "CronTab",
			},
			Scope: apiextensions.NamespaceScoped,
		},
	}
	Eventually(func() error {
		return K8sClient.Create(ctx, &crontab)
	}).Should(Succeed())
}

// getNumPropagatedObjects returns NumPropagatedObjects status for a given type. If NumPropagatedObjects is
// not set or if type does not exist in status, it returns -1 and an error.
func getNumPropagatedObjects(ctx context.Context, group, resource string) func() (int, error) {
	return func() (int, error) {
		c, err := GetHNCConfig(ctx)
		if err != nil {
			return -1, err
		}
		for _, t := range c.Status.Resources {
			if t.Group == group && t.Resource == resource {
				if t.NumPropagatedObjects != nil {
					return *t.NumPropagatedObjects, nil
				}
				return -1, fmt.Errorf("NumPropagatedObjects field is not set for "+
					"group %s, resource %s", group, resource)
			}
		}
		return -1, fmt.Errorf("group %s, resource %s is not found in status", group, resource)
	}
}

// hasNumSourceObjects returns true if NumSourceObjects is set (not nil) for a specific type and returns false
// if NumSourceObjects is not set. It returns false and an error if the type does not exist in the status.
func hasNumSourceObjects(ctx context.Context, group, resource string) func() (bool, error) {
	return func() (bool, error) {
		c, err := GetHNCConfig(ctx)
		if err != nil {
			return false, err
		}
		for _, t := range c.Status.Resources {
			if t.Group == group && t.Resource == resource {
				return t.NumSourceObjects != nil, nil
			}
		}
		return false, fmt.Errorf("group %s, resource %s is not found in status", group, resource)
	}
}

// getNumSourceObjects returns NumSourceObjects status for a given type. If NumSourceObjects is
// not set or if type does not exist in status, it returns -1 and an error.
func getNumSourceObjects(ctx context.Context, group, resource string) func() (int, error) {
	return func() (int, error) {
		c, err := GetHNCConfig(ctx)
		if err != nil {
			return -1, err
		}
		for _, t := range c.Status.Resources {
			if t.Group == group && t.Resource == resource {
				if t.NumSourceObjects != nil {
					return *t.NumSourceObjects, nil
				}
				return -1, fmt.Errorf("NumSourceObjects field is not set for "+
					"group %s, resource %s", group, resource)
			}
		}
		return -1, fmt.Errorf("group %s, resource %s is not found in status", group, resource)
	}
}
