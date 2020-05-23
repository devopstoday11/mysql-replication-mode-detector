/*
Copyright The KubeDB Authors.

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

package controller

import (
	"context"
	"fmt"
	"os"
	"strings"

	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog"
	core_util "kmodules.xyz/client-go/core/v1"
	"kmodules.xyz/client-go/tools/queue"
)

func (c *Controller) initWatcher() {
	c.podInformer = c.InitInformer()
	c.podQueue = queue.New("Pod", c.maxNumRequeues, c.numThreads, c.podLabeler)
	c.podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			if pod, ok := obj.(*core.Pod); ok {
				queue.Enqueue(c.podQueue.GetQueue(), pod)
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			if pod, ok := newObj.(*core.Pod); ok {
				queue.Enqueue(c.podQueue.GetQueue(), pod)
			}

		},
		DeleteFunc: func(obj interface{}) {
			if pod, ok := obj.(*core.Pod); ok {
				queue.Enqueue(c.podQueue.GetQueue(), pod)
			}
		},
	})
	c.podNamespaceLister = c.kubeInformerFactory.Core().V1().Pods().Lister().Pods(c.watchNamespace)
}

func (c *Controller) podLabeler(key string) error {
	klog.Infoln("Started processing, key:", key)
	obj, exists, err := c.podInformer.GetIndexer().GetByKey(key)
	if err != nil {
		klog.Errorf("Fetching object with key %s from store failed with %v", key, err)
		return err
	}

	if !exists {
		klog.Infof("Pod %s does not exist anymore", key)
	} else {
		pod := obj.(*core.Pod).DeepCopy()

		isPrimary, err := c.checkPrimary(pod.ObjectMeta)
		if err != nil {
			return err
		}
		if isPrimary {
			if err := c.ensurePrimaryRoleLabel(pod); err != nil {
				return err
			}
		} else {
			if err := c.ensureSecondaryRoleLabel(pod); err != nil {
				return err
			}
		}
		klog.Infof("Set label as role(primary/secondary) in %s/%s pod have succeeded!!", pod.Namespace, pod.Name)
	}
	return nil
}

func (c *Controller) checkPrimary(ObjMeta metav1.ObjectMeta) (bool, error) {
	user := os.Getenv(KeyMySQLUser)
	if user == "" {
		return false, fmt.Errorf("missing 'MYSQL_ROOT_USERNAME' env in MySQL Pod")
	}
	password := os.Getenv(KeyMySQLPassword)
	if user == "" {
		return false, fmt.Errorf("missing 'MYSQL_ROOT_PASSWORD' env in MySQL Pod")
	}

	// MySQL query to check master
	query := `SELECT MEMBER_HOST FROM performance_schema.replication_group_members
	INNER JOIN performance_schema.global_status ON (MEMBER_ID = VARIABLE_VALUE)
	WHERE VARIABLE_NAME='group_replication_primary_member';`
	result, err := c.queryInMySQLDatabase(ObjMeta, user, password, query)
	if err != nil {
		return false, err
	}

	host := string(result[0]["MEMBER_HOST"])
	hostName := strings.Split(host, ".")[0]

	if hostName == ObjMeta.Name {
		return true, nil
	}

	return false, nil
}

func (c *Controller) ensurePrimaryRoleLabel(pod *core.Pod) error {
	_, _, err := core_util.PatchPod(context.TODO(), c.kubeClient, pod, func(in *core.Pod) *core.Pod {
		delete(in.Labels, LabelRole)
		in.Labels = core_util.UpsertMap(in.Labels, map[string]string{
			LabelRole: Primary,
		})
		return in
	}, metav1.PatchOptions{})
	return err
}

func (c *Controller) ensureSecondaryRoleLabel(pod *core.Pod) error {
	_, _, err := core_util.PatchPod(context.TODO(), c.kubeClient, pod, func(in *core.Pod) *core.Pod {
		delete(pod.Labels, LabelRole)
		in.Labels = core_util.UpsertMap(in.Labels, map[string]string{
			LabelRole: Secondary,
		})
		return in
	}, metav1.PatchOptions{})
	return err
}