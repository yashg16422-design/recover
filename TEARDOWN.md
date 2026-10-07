# TEARDOWN: remove every AWS resource this project creates

Run this when you are done (or if anything goes wrong). Everything lives in **one region** and is tagged `Project=recover`.
Order matters: dependents first (instance before security group, stack before bucket).

```bash
export AWS_PROFILE=recover
export AWS_REGION=ap-southeast-2
export AWS_DEFAULT_REGION=ap-southeast-2
export ACCOUNT_ID=$(aws sts get-caller-identity --query Account --output text)
```

If any command says the token is expired or invalid, STOP and re-run `aws login --profile recover --remote`, then continue.

Resource names used by this project (fixed on purpose): stack `recover-fraud`, bucket `recover-fraud-artifacts-<ACCOUNT_ID>`,
security group `recover-sg`, key pair `recover-key`, role `recover-ec2-role`, instance profile `recover-ec2-profile`,
SSM parameters under `/recover/`, EC2 instance and Elastic IP tagged `Project=recover`.

## 0. See what exists (read-only)

```bash
aws resourcegroupstaggingapi get-resources --tag-filters Key=Project,Values=recover \
  --query 'ResourceTagMappingList[].ResourceARN' --output text | tr '\t' '\n'
```

## 1. EC2 instance (stops the biggest cost first)

```bash
INSTANCE_ID=$(aws ec2 describe-instances \
  --filters Name=tag:Project,Values=recover Name=instance-state-name,Values=pending,running,stopping,stopped \
  --query 'Reservations[].Instances[].InstanceId' --output text)
echo "instance: $INSTANCE_ID"
aws ec2 terminate-instances --instance-ids $INSTANCE_ID
aws ec2 wait instance-terminated --instance-ids $INSTANCE_ID && echo terminated
```

## 2. Elastic IP (an unattached or attached public IPv4 address bills hourly, so release it)

```bash
ALLOC_ID=$(aws ec2 describe-addresses --filters Name=tag:Project,Values=recover \
  --query 'Addresses[].AllocationId' --output text)
aws ec2 release-address --allocation-id $ALLOC_ID && echo released
```

## 3. Security group and key pair

```bash
aws ec2 delete-security-group --group-name recover-sg
aws ec2 delete-key-pair --key-name recover-key
rm -f ~/.ssh/recover-key.pem        # the private key you saved locally
```

## 4. EC2 role and instance profile

```bash
aws iam remove-role-from-instance-profile --instance-profile-name recover-ec2-profile --role-name recover-ec2-role
aws iam delete-instance-profile --instance-profile-name recover-ec2-profile
for p in $(aws iam list-role-policies --role-name recover-ec2-role --query 'PolicyNames[]' --output text); do
  aws iam delete-role-policy --role-name recover-ec2-role --policy-name $p; done
aws iam delete-role --role-name recover-ec2-role
```

## 5. Fraud service stack (Lambda, HTTP API, log group, EventBridge keep-warm rule, Lambda role)

```bash
sam delete --stack-name recover-fraud --region ap-southeast-2 --profile recover --no-prompts
```

## 6. Artifact bucket

```bash
aws s3 rm s3://recover-fraud-artifacts-$ACCOUNT_ID --recursive
aws s3 rb s3://recover-fraud-artifacts-$ACCOUNT_ID
```

## 7. SSM parameters (the fraud API secret and any app secrets)

```bash
aws ssm describe-parameters --parameter-filters Key=Name,Option=BeginsWith,Values=/recover/ --query 'Parameters[].Name' --output text
aws ssm delete-parameters --names $(aws ssm describe-parameters \
  --parameter-filters Key=Name,Option=BeginsWith,Values=/recover/ --query 'Parameters[].Name' --output text)
```

## 8. SAM's own deployment bucket (created automatically by `sam deploy --guided` in the first deploy)

Skip this if you want to keep using SAM in this account; it costs almost nothing. To remove it:

```bash
SAMBUCKET=$(aws cloudformation describe-stacks --stack-name aws-sam-cli-managed-default \
  --query 'Stacks[0].Outputs[?OutputKey==`SourceBucket`].OutputValue' --output text)
echo $SAMBUCKET
aws s3 rm s3://$SAMBUCKET --recursive
# the bucket is versioned: delete old versions too (skip if the command below prints an error about no versions)
aws s3api list-object-versions --bucket $SAMBUCKET --output json \
  --query '{Objects: Versions[].{Key:Key,VersionId:VersionId}}' > /tmp/recover_versions.json
aws s3api delete-objects --bucket $SAMBUCKET --delete file:///tmp/recover_versions.json
aws cloudformation delete-stack --stack-name aws-sam-cli-managed-default
aws cloudformation wait stack-delete-complete --stack-name aws-sam-cli-managed-default && echo deleted
```

## 9. Verify nothing is left

```bash
aws ec2 describe-instances --filters Name=tag:Project,Values=recover --query 'Reservations[].Instances[].[InstanceId,State.Name]' --output text
aws ec2 describe-addresses --query 'Addresses[].[PublicIp,AllocationId]' --output text
aws cloudformation list-stacks --stack-status-filter CREATE_COMPLETE UPDATE_COMPLETE --query 'StackSummaries[].StackName' --output text
aws lambda list-functions --query 'Functions[].FunctionName' --output text
aws s3 ls
aws logs describe-log-groups --query 'logGroups[].logGroupName' --output text
aws resourcegroupstaggingapi get-resources --tag-filters Key=Project,Values=recover --query 'ResourceTagMappingList[].ResourceARN' --output text
```

Each should print nothing for this project (other items may show if you created them for something else).

## 10. Check billing and log out

- Console > Billing and Cost Management > Cost Explorer: look again the next day to confirm no new charges (billing data lags by about a day).
- `aws logout --profile recover` ends the CLI session. Credentials also expire on their own after about 12 hours.
- Optional: if you do not plan to use the account further, close it from the console's account settings (this is permanent).
