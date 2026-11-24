using Commerce.Domain;
using Commerce.Domain.Ports;
using Microsoft.EntityFrameworkCore;

namespace Commerce.Infrastructure.Persistence.Repositories;

public sealed class OfferRatingReader(CommerceDbContext db) : IOfferRatingReader
{
    public async Task<OfferRating> GetAsync(string offerId, CancellationToken cancellationToken)
    {
        var exists = await db.Database
            .SqlQueryRaw<bool>("SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'offer_ratings') AS \"Value\"")
            .SingleAsync(cancellationToken);
        if (!exists)
        {
            return new OfferRating(0, 0);
        }

        var rows = await db.Database
            .SqlQueryRaw<RatingRow>(
                "SELECT COALESCE(avg_rating, 0) AS average_rating, COALESCE(reviews_count, 0) AS reviews_count FROM public.offer_ratings WHERE offer_id = {0}",
                offerId)
            .ToListAsync(cancellationToken);
        var row = rows.FirstOrDefault();
        return row is null ? new OfferRating(0, 0) : new OfferRating(row.AverageRating, row.ReviewsCount);
    }

    private sealed class RatingRow
    {
        public double AverageRating { get; set; }
        public int ReviewsCount { get; set; }
    }
}
