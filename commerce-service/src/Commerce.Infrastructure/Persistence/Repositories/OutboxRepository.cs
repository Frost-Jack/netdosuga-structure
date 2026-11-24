using Commerce.Domain.Entities;
using Commerce.Domain.Ports;
using Microsoft.EntityFrameworkCore;

namespace Commerce.Infrastructure.Persistence.Repositories;

public sealed class OutboxRepository(CommerceDbContext db) : IOutboxRepository
{
    public void Add(OutboxEvent @event) => db.OutboxEvents.Add(@event);

    public async Task<IReadOnlyList<OutboxEvent>> ListUnpublishedAsync(int limit, CancellationToken cancellationToken)
    {
        var items = await db.OutboxEvents
            .Where(x => x.PublishedAt == null)
            .OrderBy(x => x.CreatedAt)
            .Take(limit)
            .ToListAsync(cancellationToken);
        return items;
    }

    public async Task MarkPublishedAsync(string eventId, CancellationToken cancellationToken)
    {
        var item = await db.OutboxEvents.FirstOrDefaultAsync(x => x.EventId == eventId, cancellationToken);
        if (item is not null)
        {
            item.PublishedAt = DateTimeOffset.UtcNow;
        }
    }
}
