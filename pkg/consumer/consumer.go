package consumer

import (
	"context"
	"encoding/json"
	"time"

	"github.com/aws/aws-sdk-go/service/autoscaling"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/azihsoyn/k8s-asg-lifecycle-manager/pkg/constants"
	"github.com/azihsoyn/k8s-asg-lifecycle-manager/pkg/node"
	"github.com/azihsoyn/k8s-asg-lifecycle-manager/pkg/queue"
	"github.com/azihsoyn/k8s-asg-lifecycle-manager/pkg/types"
	log "github.com/sirupsen/logrus"
)

// Consumer a consumer worker thread
type Consumer struct {
	ASG   *autoscaling.AutoScaling
	Base  *types.Base
	EC2   *ec2.EC2
	Queue *queue.Queue
}

// Start start the worker thread
func (c Consumer) Start(ctx context.Context) error {
	ticker := time.Tick(1 * time.Second)
	go func() {
		for {
			select {
			case <-ticker:
				msg, err := c.Queue.Read()
				if err != nil {
					log.Error(err.Error())
					continue
				}

				if msg == nil {
					log.Info("No messages in queue")
					continue
				}

				event, err := msg.Body()
				if err != nil {
					log.Error(err.Error())
					continue
				}

				// determine if we care about this event
				if event.LifecycleTransition != constants.InstanceTerminating {
					log.Infof("Received lifecycle transition %s. Ignoring...", event.LifecycleTransition)
					err = msg.Delete()
					if err != nil {
						log.Error(err.Error())
					}
					continue
				}

				log.Infof("Received autoscaling lifecycle termination event: ")
				s, _ := json.MarshalIndent(event, "", "  ")
				log.Infof("%s", string(s))

				// create node struct from the ec2 id in the parsed message
				n, err := node.New(c.EC2, event.EC2InstanceID, c.Base.Config.ShortHostname)
				if err != nil {
					log.Error(err.Error())
					continue
				}

				state, _ := n.State()
				if state != constants.InstanceTerminatedState {
					err = n.Drain()
					if err != nil {
						log.Error(err.Error())
						msg.Visibility()
						continue
					}
				} else {
					log.Warnf("Instance %s has already been terminated. Deleting...", event.EC2InstanceID)
				}

				err = n.Delete()
				if err != nil {
					log.Info(err.Error())
				}

				err = msg.Delete()
				if err != nil {
					log.Error(err.Error())
				}

				// can't pass constant to func so need read it into a var before passing
				cont := constants.AsgActionContinue

				// tell the ASG it's ok to proceed with the action
				log.Infof("Completing lifecycle action for instance %s", event.EC2InstanceID)
				_, err = c.ASG.CompleteLifecycleAction(&autoscaling.CompleteLifecycleActionInput{
					AutoScalingGroupName:  &event.AutoScalingGroupName,
					LifecycleActionResult: &cont,
					LifecycleActionToken:  &event.LifecycleActionToken,
					LifecycleHookName:     &event.LifecycleHookName,
				})
				if err != nil {
					log.Warnf("%s. Continuing ...", err.Error())
				}

			case <-ctx.Done():
				return
			}
		}
	}()
	return nil
}
