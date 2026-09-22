import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('../api.ts', () => ({ apiPost: vi.fn() }));

import { apiPost } from '../api.ts';
import { invokeCardAction } from '../cardActions.ts';

describe('invokeCardAction', () => {
  beforeEach(() => {
    vi.mocked(apiPost).mockReset();
  });

  it('posts the declared handler and payload to the card action endpoint', async () => {
    vi.mocked(apiPost).mockResolvedValue({ accepted: true });

    await invokeCardAction('card-123', 'approve', { decision: 'yes' });

    expect(apiPost).toHaveBeenCalledWith('/cards/card-123/actions', {
      handler: 'approve',
      payload: { decision: 'yes' },
    });
  });

  it('surfaces an undeclared-handler 422 failure to the caller', async () => {
    vi.mocked(apiPost).mockRejectedValue(new Error('handler is not declared for this card'));

    await expect(invokeCardAction('card-123', 'not-declared', {})).rejects.toThrow(
      'handler is not declared for this card',
    );
  });
});
