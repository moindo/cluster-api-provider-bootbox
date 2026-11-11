#!/usr/bin/env python3
"""Fail on YAML that strict parsers reject (duplicate mapping keys).

Helm and yq accept duplicate keys; Flux's post-renderer does not, so a chart
can render locally and still fail to install. Reads multi-document YAML from
stdin.
"""
import sys
import yaml


class StrictLoader(yaml.SafeLoader):
    def construct_mapping(self, node, deep=False):
        seen = set()
        for key_node, _ in node.value:
            key = self.construct_object(key_node, deep=deep)
            if key in seen:
                raise yaml.constructor.ConstructorError(
                    None, None, f"duplicate key {key!r}", key_node.start_mark)
            seen.add(key)
        return super().construct_mapping(node, deep=deep)


try:
    n = sum(1 for _ in yaml.load_all(sys.stdin, Loader=StrictLoader))
except yaml.YAMLError as e:
    sys.exit(f"invalid YAML: {e}")
print(f"{n} documents ok")
