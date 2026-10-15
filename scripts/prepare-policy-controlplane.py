#!/usr/bin/env python3
"""Prepare a static CloudFormation candidate; this script never deploys."""
import argparse
import copy
import json
from pathlib import Path

PATHS = (
    "/v1alpha1/policies",
    "/v1alpha1/policies/*",
    "/v1alpha1/sync",
    "/v1alpha1/dataplanes",
    "/ui/auth/config",
)


def prepare(source):
    source = source.get("TemplateBody", source)
    if isinstance(source, str):
        source = json.loads(source)
    result = copy.deepcopy(source)
    config = result["Resources"]["Distribution"]["Properties"]["DistributionConfig"]
    default = config["DefaultCacheBehavior"]
    origin = next(o for o in config["Origins"] if o["Id"] == default["TargetOriginId"])
    if "VpcOriginConfig" not in origin:
        raise ValueError("The existing CP must use the reviewed NLB VPC origin")
    if any(o["Id"] == "policy-cp" for o in config["Origins"]):
        raise ValueError("Policy origin already exists; review updates explicitly")
    if "PolicyVpcOriginId" in result.get("Parameters", {}):
        raise ValueError("Policy origin parameter already exists")
    if any(b.get("PathPattern") in PATHS for b in config.get("CacheBehaviors", [])):
        raise ValueError("An exact policy behavior already exists")
    result.setdefault("Parameters", {})["PolicyVpcOriginId"] = {
        "Type": "String", "AllowedPattern": "vo_[A-Za-z0-9]+",
        "Description": "Provisioned private policy CP VPC origin; no usage routing change",
    }
    new_origin = copy.deepcopy(origin)
    new_origin["Id"] = "policy-cp"
    new_origin["VpcOriginConfig"]["VpcOriginId"] = {"Ref": "PolicyVpcOriginId"}
    config["Origins"].append(new_origin)
    added = []
    for path in PATHS:
        behavior = copy.deepcopy(default)
        behavior.update(PathPattern=path, TargetOriginId="policy-cp")
        added.append(behavior)
    config["CacheBehaviors"] = added + config.get("CacheBehaviors", [])
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--template", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    candidate = prepare(json.loads(args.template.read_text()))
    args.output.write_text(json.dumps(candidate, indent=2) + "\n")
    args.output.chmod(0o600)
    print("Prepared policy-only routing candidate; all other resources and usage routing preserved")


if __name__ == "__main__":
    main()
