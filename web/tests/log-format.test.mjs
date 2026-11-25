import assert from 'node:assert/strict';
import test from 'node:test';

import {
    isLogFailure,
    mergeAdjacentAttempts,
    resolveTokenUsageDisplay,
} from '../src/components/modules/log/log-format.ts';

test('log failure uses the final status even when the error text is empty', () => {
    assert.equal(isLogFailure({ success: false, error: '' }), true);
    assert.equal(isLogFailure({ success: false, error: 'empty upstream response' }), true);
    assert.equal(isLogFailure({ success: true, error: '' }), false);
    assert.equal(isLogFailure({ success: true, error: 'previous attempt failed' }), false);
    assert.equal(isLogFailure({ error: 'legacy failure' }), true);
    assert.equal(isLogFailure({ error: '' }), false);
});

function attempt(attemptNum, overrides = {}) {
    return {
        channel_id: 1,
        channel_key_id: 11,
        channel_name: 'Primary',
        model_name: 'gpt-test',
        attempt_num: attemptNum,
        status: 'failed',
        duration: 100,
        msg: 'upstream unavailable',
        ...overrides,
    };
}

test('merged retries retain their order, count and duration without changing source logs', () => {
    const attempts = [
        attempt(1),
        attempt(2, { duration: 250 }),
        attempt(3, { channel_id: 2, channel_key_id: 22 }),
        attempt(4),
    ];
    const original = structuredClone(attempts);
    const merged = mergeAdjacentAttempts(attempts);

    assert.deepEqual(merged.map(item => item.attempt_num), [1, 3, 4]);
    assert.deepEqual(merged.map(item => item.repeat), [2, 1, 1]);
    assert.equal(merged[0].totalDuration, 350);
    assert.equal(merged[0].lastAttemptNum, 2);
    assert.deepEqual(attempts, original);
});

test('retries with different keys, models or outcomes remain separate', () => {
    for (const change of [
        { channel_id: 2 },
        { channel_key_id: 22 },
        { model_name: 'another-model' },
        { status: 'success' },
        { msg: 'different failure' },
        { failure_scope: 'key' },
        { selection_reason: 'channel_affinity' },
        { routing: { stop_reason: 'budget_exceeded', attempts_used: 2 } },
    ]) {
        assert.equal(mergeAdjacentAttempts([attempt(1), attempt(2, change)]).length, 2);
    }
    assert.deepEqual(mergeAdjacentAttempts([]), []);
});

test('token totals use normalized billing and cache accounting', () => {
    const base = {
        outputTokens: 20,
        billInputTokens: 100,
        cacheReadTokens: 40,
        cacheWriteTokens: 0,
    };
    const cases = [
        { name: 'non-cached input', changes: {}, nonCached: 100, input: 140, total: 160 },
        { name: 'cache write', changes: { cacheWriteTokens: 10 }, nonCached: 100, input: 150, total: 170 },
        { name: 'normalized billing', changes: { billInputTokens: 7 }, nonCached: 7, input: 47, total: 67 },
        { name: 'zero is a known billed count', changes: { billInputTokens: 0 }, nonCached: 0, input: 40, total: 60 },
        { name: 'missing usage', changes: { billInputTokens: null }, nonCached: 0, input: 40, total: 60 },
        { name: 'negative counts', changes: { billInputTokens: -5, outputTokens: -2, cacheReadTokens: -3, cacheWriteTokens: -4 }, nonCached: 0, input: 0, total: 0 },
    ];
    for (const { name, changes, nonCached, input, total } of cases) {
        const usage = resolveTokenUsageDisplay({ ...base, ...changes });
        assert.equal(usage.nonCachedInputTokens, nonCached, name);
        assert.equal(usage.totalInputTokens, input, name);
        assert.equal(usage.totalTokens, total, name);
    }
});
