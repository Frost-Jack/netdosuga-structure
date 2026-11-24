using Commerce.Domain;
using Commerce.Domain.Entities;
using Commerce.Domain.Ports;
using Microsoft.EntityFrameworkCore;

namespace Commerce.Infrastructure.Persistence.Repositories;

public sealed class OfferRepository(CommerceDbContext db) : IOfferRepository
{
    public async Task<IReadOnlyList<Offer>> ListPublishedAsync(string city, string category, CancellationToken cancellationToken)
    {
        var items = await db.Offers
            .Where(x => x.Status == OfferStatus.Published)
            .Where(x => city == "" || x.City == city)
            .Where(x => category == "" || x.Category == category)
            .OrderBy(x => x.Id)
            .Take(200)
            .ToListAsync(cancellationToken);
        return items;
    }

    public async Task<IReadOnlyList<Offer>> ListAsync(string actorId, bool includeAll, CancellationToken cancellationToken)
    {
        var items = await db.Offers
            .Where(x => includeAll || x.VendorId == actorId)
            .OrderBy(x => x.Id)
            .Take(200)
            .ToListAsync(cancellationToken);
        return items;
    }

    public Task<Offer?> GetAsync(string id, CancellationToken cancellationToken) =>
        db.Offers.FirstOrDefaultAsync(x => x.Id == id, cancellationToken);

    public void Add(Offer offer) => db.Offers.Add(offer);

    public async Task SetPhotoPathAsync(string id, string photoPath, CancellationToken cancellationToken)
    {
        var offer = await db.Offers.FirstOrDefaultAsync(x => x.Id == id, cancellationToken)
            ?? throw new DomainException(DomainError.NotFound());
        offer.PhotoPath = photoPath;
    }
}
