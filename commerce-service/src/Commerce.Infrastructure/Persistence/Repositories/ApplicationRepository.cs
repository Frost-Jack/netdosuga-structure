using Commerce.Domain.Entities;
using Commerce.Domain.Ports;
using Microsoft.EntityFrameworkCore;

namespace Commerce.Infrastructure.Persistence.Repositories;

public sealed class ApplicationRepository(CommerceDbContext db) : IApplicationRepository
{
    public async Task<IReadOnlyList<Domain.Entities.Application>> ListAsync(string actorId, bool includeAll, CancellationToken cancellationToken)
    {
        var items = await db.Applications
            .Where(x => includeAll || x.VendorId == actorId)
            .OrderBy(x => x.Id)
            .Take(200)
            .ToListAsync(cancellationToken);
        return items;
    }

    public Task<Domain.Entities.Application?> GetAsync(string id, CancellationToken cancellationToken) =>
        db.Applications.FirstOrDefaultAsync(x => x.Id == id, cancellationToken);

    public void Add(Domain.Entities.Application application) => db.Applications.Add(application);

    public async Task<IReadOnlyList<VendorSummary>> ListVendorsAsync(CancellationToken cancellationToken)
    {
        var applications = await db.Applications
            .GroupBy(x => x.VendorId)
            .Select(group => new
            {
                Id = group.Key,
                Organization = group.OrderByDescending(x => x.CreatedAt).Select(x => x.Organization).FirstOrDefault() ?? group.Key,
                Applications = group.Count()
            })
            .ToListAsync(cancellationToken);
        var offers = await db.Offers
            .GroupBy(x => x.VendorId)
            .Select(group => new { Id = group.Key, Offers = group.Count() })
            .ToListAsync(cancellationToken);
        return applications
            .Select(item => new VendorSummary(
                item.Id,
                item.Organization,
                item.Applications,
                offers.FirstOrDefault(x => x.Id == item.Id)?.Offers ?? 0))
            .ToList();
    }
}
