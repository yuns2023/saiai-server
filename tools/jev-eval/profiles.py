"""Explicit dataset profiles; switching profiles never carries authorization."""
import json
from validate import BOUNDARY, FIXTURE, validate_suite
from followup import FOLLOWUP, validate_followup

PROFILES = ('expanded-108', 'language-32')


def load_profile(name):
    if name == 'expanded-108':
        paths, suite, limit = (FIXTURE, BOUNDARY), validate_suite(), 108
    elif name == 'language-32':
        details = validate_followup()
        paths, limit = (FOLLOWUP,), 32
        suite = {'samples': details['samples'], 'scenario_families': details['scenario_families'],
                 'contrast_groups': details['contrast_groups'], 'datasets': [details]}
    else:
        raise ValueError('unknown evaluation profile')
    rows = [json.loads(line) for p in paths for line in p.read_text().splitlines() if line.strip()]
    if len(rows) != limit:
        raise ValueError('profile sample count changed; define a new scope')
    return rows, suite, limit
