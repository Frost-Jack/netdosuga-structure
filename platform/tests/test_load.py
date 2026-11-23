"""Offline checks; never send HTTP requests."""
import contextlib
import io
import os
import unittest
from unittest.mock import patch

import load


class LoadTests(unittest.TestCase):
    def test_stats_nearest_rank_and_empty(self):
        self.assertEqual(load.latency_stats([])['p95'], None)
        stats = load.latency_stats([.001, .002, .003, .004])
        self.assertEqual(stats, {'avg': 2.5, 'max': 4.0, 'p50': 2.0, 'p95': 4.0, 'p99': 4.0})

    def test_invalid_limits_and_origins_do_not_request(self):
        cases = [(['--rate', 'nan'], 'http://gateway:8080'),
                 (['--duration', '121'], 'http://gateway:8080'),
                 (['--duration', '120', '--rate', '100'], 'http://gateway:8080'),
                 (['--concurrency', '0'], 'http://gateway:8080'),
                 (['--timeout', '11'], 'http://gateway:8080'),
                 ([], 'http://example.com:8080'), ([], 'http://gateway:8080/foo'),
                 ([], 'http://user:password@gateway:8080'), ([], 'http://gateway:8080?x=1')]
        with patch('load.request') as request:
            for argv, url in cases:
                with self.subTest(argv=argv, url=url), patch.dict(os.environ, GATEWAY_URL=url):
                    with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit) as failure:
                        load.arguments(argv)
                    self.assertEqual(failure.exception.code, 2)
            request.assert_not_called()

    def test_defaults(self):
        with patch.dict(os.environ, GATEWAY_URL='http://gateway:8080'):
            args = load.arguments([])
        self.assertEqual((args.duration, args.concurrency, args.rate), (15, 4, 10))
        self.assertEqual(args.url, 'http://gateway:8080/api/v1/shop/catalog')


if __name__ == '__main__':
    unittest.main()
