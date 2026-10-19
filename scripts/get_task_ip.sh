#!/usr/bin/env bash
# Prints the public IP of the running ECS task.
# Usage: ./scripts/get_task_ip.sh <cluster-name> <service-name>
# (names come from `terraform output` in terraform/)
set -euo pipefail
CLUSTER=$1; SERVICE=$2; REGION=${AWS_REGION:-us-west-2}
TASK=$(aws ecs list-tasks --cluster "$CLUSTER" --service-name "$SERVICE" --region "$REGION" --query 'taskArns[0]' --output text)
ENI=$(aws ecs describe-tasks --cluster "$CLUSTER" --tasks "$TASK" --region "$REGION" \
  --query "tasks[0].attachments[0].details[?name=='networkInterfaceId'].value" --output text)
aws ec2 describe-network-interfaces --network-interface-ids "$ENI" --region "$REGION" \
  --query 'NetworkInterfaces[0].Association.PublicIp' --output text
