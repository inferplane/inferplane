"""Launch a SageMaker training job for one backend (no SageMaker SDK needed).

Usage: python3 launch.py --backend laya|strands --bucket BUCKET --role-arn ROLE_ARN \
           [--region us-east-1] [--instance ml.g5.2xlarge] [--hp epochs=4] [--wait]
Data must already be at s3://<bucket>/data/ (neutral/ and backends/).
Credentials come from the default AWS credential chain unless --profile is given.
"""

from __future__ import annotations

import argparse
import io
import os
import tarfile
import time

import boto3

IMAGE = "763104351884.dkr.ecr.{region}.amazonaws.com/pytorch-training:2.7.1-gpu-py312-cu128-ubuntu22.04-sagemaker"
DEFAULT_INSTANCE = {"laya": "ml.g5.2xlarge", "strands": "ml.g6e.xlarge"}


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--backend", required=True, choices=["laya", "strands"])
    ap.add_argument("--instance")
    ap.add_argument("--hp", action="append", default=[], help="key=value")
    ap.add_argument("--profile", default=None)
    ap.add_argument("--max-hours", type=float, default=8)
    ap.add_argument("--wait", action="store_true")
    ap.add_argument("--data-prefix", default="data/")
    ap.add_argument("--region", default="us-east-1", help="AWS region, for example us-east-1")
    ap.add_argument("--bucket", required=True, help="private S3 bucket that holds data/ and job outputs")
    ap.add_argument("--role-arn", required=True, help="SageMaker execution role scoped to --bucket")
    a = ap.parse_args()
    REGION, BUCKET = a.region, a.bucket

    s = boto3.Session(profile_name=a.profile, region_name=REGION)
    s3, sm = s.client("s3"), s.client("sagemaker")
    name = f"pii-{a.backend}-{time.strftime('%Y%m%d-%H%M%S')}"

    buf = io.BytesIO()
    here = os.path.dirname(os.path.abspath(__file__))
    with tarfile.open(fileobj=buf, mode="w:gz") as tar:
        for fn in ("sm_train.py", "score_http.py"):
            tar.add(os.path.join(here, fn), arcname=fn)
    src_key = f"jobs/{name}/source/sourcedir.tar.gz"
    s3.put_object(Bucket=BUCKET, Key=src_key, Body=buf.getvalue())

    hps = {"backend": a.backend, **dict(kv.split("=", 1) for kv in a.hp)}
    hps.update({"sagemaker_program": "sm_train.py", "sagemaker_submit_directory": f"s3://{BUCKET}/{src_key}",
                "sagemaker_region": REGION})
    sm.create_training_job(
        TrainingJobName=name,
        AlgorithmSpecification={"TrainingImage": IMAGE.format(region=REGION), "TrainingInputMode": "File"},
        RoleArn=a.role_arn,
        HyperParameters={k: str(v) for k, v in hps.items()},
        InputDataConfig=[{"ChannelName": "data", "DataSource": {"S3DataSource": {
            "S3DataType": "S3Prefix", "S3Uri": f"s3://{BUCKET}/{a.data_prefix}", "S3DataDistributionType": "FullyReplicated"}}}],
        OutputDataConfig={"S3OutputPath": f"s3://{BUCKET}/jobs/"},
        ResourceConfig={"InstanceType": a.instance or DEFAULT_INSTANCE[a.backend], "InstanceCount": 1, "VolumeSizeInGB": 150},
        StoppingCondition={"MaxRuntimeInSeconds": int(a.max_hours * 3600)},
        Tags=[{"Key": "project", "Value": "inferplane-pii-ft"}],
    )
    print(name, flush=True)
    if a.wait:
        while True:
            d = sm.describe_training_job(TrainingJobName=name)
            st = d["TrainingJobStatus"]
            if st in ("Completed", "Failed", "Stopped"):
                print(st, d.get("FailureReason", ""), d.get("ModelArtifacts", {}).get("S3ModelArtifacts", ""))
                break
            time.sleep(60)


if __name__ == "__main__":
    main()
