# Deploying the fraud service to AWS Lambda (optional)

**Nothing here has been run.** No AWS resource exists unless you run these steps yourself.
The service works locally without any of this (`make fraud-serve`).

## What gets deployed

```
Go router --FRAUD_URL--> API Gateway (HTTP API) --> Lambda (FastAPI + Mangum, numpy only)
                                                        |  cold start: download 5 small files
                                                        v
                                                  S3  s3://<bucket>/fraud-artifacts/
```

- One Lambda (python3.12, arm64, 512 MB, 10 s timeout), one HTTP API, one log group (7-day retention).
- IAM: `s3:GetObject` on `s3://<bucket>/<prefix>/*` only. Nothing else.
- Artifacts are **not** inside the zip. They are loaded from S3 once per container (cold start), then served from memory.
- Measured package (`fraud/artifacts/results.json` -> `lambda_package`): **80.9 MB unzipped, 25.1 MB zipped, 2043 files**
  (limits: 250 MB / 50 MB). numpy is most of it. Artifacts in S3: 3.35 MB.
  Built locally from Linux arm64 wheels; **not yet run on real Lambda**.

## Prerequisites (you install these)

- AWS CLI v2 and AWS SAM CLI. `docker` is already on this Mac and is needed for `--use-container`.
- A local AWS profile. Use `aws configure --profile fraud-demo` yourself. **Never put keys in a file in this repo.**

```bash
export AWS_PROFILE=fraud-demo
export AWS_REGION=ap-south-1        # your choice
aws sts get-caller-identity         # confirm you are in the right account
```

## 1. Create the bucket and upload the artifacts

```bash
BUCKET=fraud-artifacts-$(aws sts get-caller-identity --query Account --output text)-demo
aws s3 mb s3://$BUCKET
cd fraud/artifacts
aws s3 cp scores.npy      s3://$BUCKET/fraud-artifacts/scores.npy
aws s3 cp scores_lr.npy   s3://$BUCKET/fraud-artifacts/scores_lr.npy
aws s3 cp labels.npy      s3://$BUCKET/fraud-artifacts/labels.npy
aws s3 cp split.npy       s3://$BUCKET/fraud-artifacts/split.npy
aws s3 cp neighbors.npz   s3://$BUCKET/fraud-artifacts/neighbors.npz
aws s3 cp results.json    s3://$BUCKET/fraud-artifacts/results.json
```

## 2. Build and deploy

```bash
cd fraud/serve
sam build --use-container      # builds Linux wheels in Docker; a plain `sam build` on a Mac would package Mac wheels
sam deploy --guided            # stack name: fraud-demo; ArtifactBucket=$BUCKET; ArtifactPrefix=fraud-artifacts
```

Answer "y" to creating the IAM role SAM generates. When it finishes, copy the **BaseUrl** output.

## 3. Point everything at it

```bash
# router (Recover server):
FRAUD_URL=https://<BaseUrl> make run
# smoke test:
curl https://<BaseUrl>/health
```

Postman: import `fraud/postman_collection.json` and `fraud/postman_env_aws.json`, set `base_url` to the BaseUrl, pick
the **fraud-aws** environment (top right), run the collection. (Import: Postman -> Import -> select the files.)

## 4. Measure cold vs warm (do this, don't guess)

The first request after ~15 minutes idle is a cold start. In CloudWatch Logs, the `REPORT` line shows
`Init Duration` (cold start only) and `Duration`. Or in Postman run request 1 once cold, then again immediately (warm)
and compare response times. Record both in your notes. No cold/warm number exists for this project yet.

## 5. Security note

The HTTP API is **public**: anyone with the URL can query it. The data is Bitcoin-dataset scores only, so this is
acceptable for a demo. For anything real, add an API key / IAM auth / JWT authorizer, and a throttle.

## 6. Tear down (so nothing keeps costing money)

```bash
cd fraud/serve && sam delete
aws s3 rm s3://$BUCKET --recursive && aws s3 rb s3://$BUCKET
```

## Estimated free-tier usage (an estimate, check current AWS pricing)

- Lambda: 1M requests and 400,000 GB-seconds per month are free (always-free tier). At 0.5 GB, 400,000 GB-s is
  800,000 compute-seconds; if a warm request took 50 ms (assumed, not measured on Lambda), that is 16M requests,
  so the **1M request cap binds first**.
- API Gateway HTTP API: 1M calls/month free for the first 12 months of a new account.
- S3: a few MB stored, a handful of GETs per cold start. Well inside the free tier.
- CloudWatch Logs: a few KB per request, 7-day retention.
- Demo traffic (Postman runs, a few hundred requests) is far below all of these.
